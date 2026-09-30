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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// shouldRotate checks if rotation is needed
func (f *FileAdapter) shouldRotate(additionalBytes int64) bool {
	if f.maxSize <= 0 {
		return false
	}
	return f.currentSize.Load()+additionalBytes > f.maxSize
}

// rotateLocked performs rotation (caller must hold batchMu)
func (f *FileAdapter) rotateLocked() error {
	f.metrics.RotationsTotal.Add(1)

	f.fileMu.Lock()
	defer f.fileMu.Unlock()

	// Close current file
	if f.currentFile != nil {
		_ = f.currentFile.Close()
		f.currentFile = nil
	}

	// Rename with timestamp
	backupName := f.path + "." + time.Now().Format("2006-01-02T15-04-05")
	if err := os.Rename(f.path, backupName); err != nil {
		return fmt.Errorf("failed to rename log file: %w", err)
	}

	// Open new file
	if err := f.openFile(); err != nil {
		return fmt.Errorf("failed to open new log file: %w", err)
	}

	// Async compression and cleanup, tracked so Close waits for it. An
	// untracked goroutine here raced shutdown: the process (or a test's temp
	// dir) could tear the backup file down while compression was mid-read.
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.rotateAsync(backupName)
	}()

	return nil
}

// rotateAsync handles compression and cleanup
func (f *FileAdapter) rotateAsync(backupFile string) {
	// Compress if enabled
	if f.compress {
		compressed := backupFile + ".gz"
		if err := compressFile(backupFile, compressed); err != nil {
			_, _ = os.Stderr.WriteString("Compression failed: ")
			_, _ = os.Stderr.WriteString(err.Error())
			_, _ = os.Stderr.WriteString("\n")
		} else {
			_ = os.Remove(backupFile)
		}
	}

	// Cleanup old backups
	if err := f.cleanupOldBackups(); err != nil {
		_, _ = os.Stderr.WriteString("Cleanup failed: ")
		_, _ = os.Stderr.WriteString(err.Error())
		_, _ = os.Stderr.WriteString("\n")
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
