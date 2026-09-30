// Copyright 2025 Admilson B. F. Cossa
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package file

// Rotation, compression, and retention of the adapter's log file.

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// shouldRotate checks if rotation is needed
func (f *FileAdapter) shouldRotate(additionalBytes int64) bool {
	if f.maxSize <= 0 {
		return false
	}
	return f.currentSize.Load()+additionalBytes > f.maxSize
}

// rotateLocked renames the log file to a unique backup name, opens a fresh
// file, and queues the backup for compression and cleanup. If the rename
// fails, the log path is reopened so writing can go on. It runs on the writer
// goroutine.
func (f *FileAdapter) rotateLocked() error {
	f.metrics.RotationsTotal.Add(1)

	backup, err := f.swapFile()
	if err != nil {
		return err
	}
	f.compressor.Add(backup)
	return nil
}

// swapFile closes the current file, renames it to a backup name, and opens a
// new file at the log path.
func (f *FileAdapter) swapFile() (string, error) {
	f.fileMu.Lock()
	defer f.fileMu.Unlock()

	if f.currentFile != nil {
		_ = f.currentFile.Close()
		f.currentFile = nil
	}

	backup := f.backupName(time.Now())
	if err := os.Rename(f.path, backup); err != nil {
		if openErr := f.openFile(); openErr != nil {
			return "", fmt.Errorf("failed to rename log file: %w; reopening it failed: %w", err, openErr)
		}
		return "", fmt.Errorf("failed to rename log file: %w", err)
	}

	if err := f.openFile(); err != nil {
		return "", fmt.Errorf("failed to open new log file: %w", err)
	}
	return backup, nil
}

// backupName returns a rotated-file name no existing backup uses: the log path
// plus a millisecond timestamp, and a counter when two rotations share one.
func (f *FileAdapter) backupName(now time.Time) string {
	base := f.path + "." + now.Format("2006-01-02T15-04-05.000")
	name := base
	for i := 1; pathExists(name) || pathExists(name+".gz"); i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	return name
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// rotateAsync compresses one rotated file and then removes backups beyond the
// retention limits. It runs on the compressor goroutine.
func (f *FileAdapter) rotateAsync(backupFile string) {
	// Compress if enabled
	if f.compress {
		compressed := backupFile + ".gz"
		err := compressFile(backupFile, compressed)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// When rotations outpace compression, retention can remove a
			// backup before its turn comes; there is nothing left to compress.
		case err != nil:
			reportError("Compression failed", err)
		default:
			_ = os.Remove(backupFile)
		}
	}

	// Cleanup old backups
	if err := f.cleanupOldBackups(); err != nil {
		reportError("Cleanup failed", err)
	}
}

// compressFile compresses a file with gzip. src and dst are internally-derived
// rotation paths (the adapter's own log file plus a timestamp suffix), never
// caller/attacker-controlled input, so the G304 file-inclusion flags are false
// positives here.
func compressFile(src, dst string) (err error) {
	srcFile, err := os.Open(src) //nolint:gosec // G304: src is an internally-derived rotated log path, not user input
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()

	// Compressed rotations hold the same log data, so keep them owner-only (0600)
	// rather than os.Create's world-readable 0666&umask default.
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, logFileMode) //nolint:gosec // G304: dst is an internally-derived rotated log path, not user input
	if err != nil {
		return err
	}
	defer func() {
		// Surface a close error only if the copy itself succeeded.
		if cerr := dstFile.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	gzipWriter := gzip.NewWriter(dstFile)
	if _, err = io.Copy(gzipWriter, srcFile); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	// Closing the gzip writer flushes the trailer; its error is actionable.
	return gzipWriter.Close()
}

// cleanupOldBackups removes old backup files
func (f *FileAdapter) cleanupOldBackups() error {
	dir := filepath.Dir(f.path)
	base := filepath.Base(f.path)

	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var backups []os.FileInfo
	cutoffTime := time.Now().AddDate(0, 0, -f.maxAge)

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := file.Name()
		if !strings.HasPrefix(name, base+".") || name == base {
			continue
		}

		info, err := file.Info()
		if err != nil {
			continue
		}

		// Remove files older than maxAge
		if f.maxAge > 0 && info.ModTime().Before(cutoffTime) {
			path := filepath.Join(dir, name)
			_ = os.Remove(path)
			continue
		}

		backups = append(backups, info)
	}

	// Sort by mod time
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ModTime().Before(backups[j].ModTime())
	})

	// Remove excess backups
	if f.maxBackups > 0 && len(backups) > f.maxBackups {
		for i := 0; i < len(backups)-f.maxBackups; i++ {
			path := filepath.Join(dir, backups[i].Name())
			_ = os.Remove(path)
		}
	}

	return nil
}

// compressor runs rotated files through compression and retention on its own
// goroutine, one file at a time and in rotation order, so no two ever touch
// the same file.
type compressor struct {
	files chan string
	done  sync.WaitGroup
}

func newCompressor(queueSize int, process func(backup string)) *compressor {
	c := &compressor{files: make(chan string, queueSize)}
	c.done.Add(1)
	go func() {
		defer c.done.Done()
		for backup := range c.files {
			process(backup)
		}
	}()
	return c
}

// Add queues a rotated file, waiting while the queue is full.
func (c *compressor) Add(backup string) {
	c.files <- backup
}

// Close processes the files already queued, then stops.
func (c *compressor) Close() {
	close(c.files)
	c.done.Wait()
}
