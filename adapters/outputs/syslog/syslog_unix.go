//go:build !windows
// +build !windows

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
// Package adapters provides output adapters
// Author: Admilson B. F. Cossa

package syslog

import (
	"bytes"
	"errors"
	"fmt"
	"log/syslog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-gen-ecosystem/halolog/adapters/formatters/text"
	"github.com/go-gen-ecosystem/halolog/types"
)

const (
	dialTimeout  = 5 * time.Second // bounds each connection attempt
	writeTimeout = 5 * time.Second // bounds each write to a stalled collector
	redialMin    = 100 * time.Millisecond
	redialMax    = 30 * time.Second // reconnection backoff doubles up to this

	facilityMask    = 0xf8
	maxRetainedLine = 64 << 10 // larger line buffers are not kept after a burst
)

// Pre-allocated errors for zero-allocation design
var (
	ErrSyslogNilEntry = errors.New("cannot write nil entry")
	ErrSyslogNotInit  = errors.New("syslog writer not initialized")
	ErrSyslogFormat   = errors.New("failed to format entry")
	ErrSyslogWrite    = errors.New("failed to write to syslog")

	// ErrSyslogClosed is returned by Write after Close.
	ErrSyslogClosed = errors.New("syslog adapter is closed")
	// ErrSyslogAddress is returned when only one of Network and Address is set.
	ErrSyslogAddress = errors.New("syslog Network and Address must be set together")

	errNoLocalSyslog = errors.New("no local syslog socket")
)

// localSockets are the local syslog sockets log/syslog tries, in order.
var localSockets = [...]string{"/dev/log", "/var/run/syslog", "/var/run/log"}

// SyslogAdapter writes log entries to syslog, locally or over the network, in
// the log/syslog wire format: `<PRI>TIMESTAMP HOSTNAME TAG[PID]: MSG`, without
// the hostname and with a time.Stamp timestamp on the local socket.
//
// Every dial and write has a deadline, so a stalled collector cannot block
// Write or Close for more than a few seconds. A lost connection is redialled
// with exponential backoff, and Write fails fast in between. Newlines inside
// a message are escaped, so a stream collector always sees one record.
//
//nolint:revive // stable cross-platform public API name; renaming would break the windows build twin and callers
type SyslogAdapter struct {
	mu        sync.Mutex
	conn      net.Conn
	network   string // "" for the local syslog socket
	address   string
	hostname  string
	pid       string
	facility  syslog.Priority
	tag       string
	formatter types.Formatter

	line, msg    []byte        // reused by every Write
	writeTimeout time.Duration // writeTimeout, shortened by tests

	configErr error         // a configuration error no redial can fix
	lastError error         // why there is no connection
	redial    time.Duration // the current reconnection backoff
	nextDial  time.Time
	closed    bool
}

// SyslogAdapterOptions contains configuration options for SyslogAdapter.
// Network and Address are set together, or both left empty for the local
// syslog socket. A zero Facility (LOG_KERN, reserved for the kernel) means
// LOG_LOCAL0. An empty Tag means the program name.
//
//nolint:revive // stable cross-platform public API name; renaming would break the windows build twin and callers
type SyslogAdapterOptions struct {
	Network   string // "", "tcp", "udp"
	Address   string // "", "host:port"
	Facility  syslog.Priority
	Tag       string
	Formatter types.Formatter
}

// NewSyslogAdapter creates a new syslog adapter
func NewSyslogAdapter(tag string) *SyslogAdapter {
	return NewSyslogAdapterWithOptions(&SyslogAdapterOptions{
		Facility: syslog.LOG_LOCAL0,
		Tag:      tag,
	})
}

// NewSyslogAdapterWithOptions creates a syslog adapter and connects it. A
// failed connection is retried by later Writes; Health reports it meanwhile.
// A nil Formatter defaults to the plain text formatter.
func NewSyslogAdapterWithOptions(options *SyslogAdapterOptions) *SyslogAdapter {
	if options == nil {
		options = &SyslogAdapterOptions{}
	}
	formatter := options.Formatter
	if formatter == nil {
		formatter = text.NewTextFormatter()
	}
	facility := options.Facility & facilityMask
	if facility == syslog.LOG_KERN {
		facility = syslog.LOG_LOCAL0
	}
	tag := options.Tag
	if tag == "" {
		tag = os.Args[0]
	}
	hostname, _ := os.Hostname()

	a := &SyslogAdapter{
		network:      options.Network,
		address:      options.Address,
		hostname:     hostname,
		pid:          strconv.Itoa(os.Getpid()),
		facility:     facility,
		tag:          tag,
		formatter:    formatter,
		writeTimeout: writeTimeout,
	}
	if (options.Network == "") != (options.Address == "") {
		a.configErr = ErrSyslogAddress
		return a
	}

	a.mu.Lock()
	_ = a.connectLocked()
	a.mu.Unlock()
	return a
}

// Name returns the name of this adapter
func (a *SyslogAdapter) Name() string {
	return "SyslogAdapter"
}

// Write sends entry to syslog. If the connection is down it redials, unless
// a recent attempt failed, in which case it returns the failure at once. A
// write that fails other than by timing out is retried once on a fresh
// connection, which covers a collector that restarted.
func (a *SyslogAdapter) Write(entry *types.LogEntry) error {
	if entry == nil {
		return ErrSyslogNilEntry
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrSyslogClosed
	}
	if a.conn == nil {
		if err := a.connectLocked(); err != nil {
			return err
		}
	}

	a.appendLineLocked(entry)
	err := a.sendLocked()
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		// The collector may have restarted: resend once on a fresh connection.
		_ = a.conn.Close()
		a.conn = nil
		if err = a.connectLocked(); err == nil {
			err = a.sendLocked()
		}
	}
	if cap(a.line) > maxRetainedLine {
		a.line, a.msg = nil, nil
	}
	if err != nil {
		if a.conn != nil { // a failed dial has already started the backoff
			a.failLocked(err)
		}
		return fmt.Errorf("%w: %w", ErrSyslogWrite, err)
	}
	return nil
}

// appendLineLocked renders entry into a.line.
func (a *SyslogAdapter) appendLineLocked(entry *types.LogEntry) {
	line := a.line[:0]
	line = append(line, '<')
	line = strconv.AppendInt(line, int64(a.facility|severity(entry.Level)), 10)
	line = append(line, '>')
	if now := time.Now(); a.network == "" {
		line = now.AppendFormat(line, time.Stamp)
	} else {
		line = now.AppendFormat(line, time.RFC3339)
		line = append(append(line, ' '), a.hostname...)
	}
	line = append(append(line, ' '), a.tag...)
	line = append(append(append(line, '['), a.pid...), "]: "...)

	a.msg = a.formatter.Format(entry, a.msg[:0])
	msg := bytes.TrimRight(a.msg, "\n")
	for {
		i := bytes.IndexByte(msg, '\n')
		if i < 0 {
			break
		}
		line = append(append(line, msg[:i]...), '\\', 'n')
		msg = msg[i+1:]
	}
	a.line = append(append(line, msg...), '\n')
}

func (a *SyslogAdapter) sendLocked() error {
	if err := a.conn.SetWriteDeadline(time.Now().Add(a.writeTimeout)); err != nil {
		return err
	}
	_, err := a.conn.Write(a.line)
	return err
}

// connectLocked dials the collector unless the backoff after a failed
// attempt is still running.
func (a *SyslogAdapter) connectLocked() error {
	if a.configErr != nil {
		return a.configErr
	}
	if time.Now().Before(a.nextDial) {
		return a.lastError
	}
	conn, err := a.dial()
	if err != nil {
		a.failLocked(fmt.Errorf("%w: %w", ErrSyslogNotInit, err))
		return a.lastError
	}
	a.conn, a.lastError, a.redial, a.nextDial = conn, nil, 0, time.Time{}
	return nil
}

// failLocked drops the connection and starts, or doubles, the backoff before
// the next dial.
func (a *SyslogAdapter) failLocked(err error) {
	if a.conn != nil {
		_ = a.conn.Close()
		a.conn = nil
	}
	a.lastError = err
	a.redial = min(max(2*a.redial, redialMin), redialMax)
	a.nextDial = time.Now().Add(a.redial)
}

func (a *SyslogAdapter) dial() (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	if a.network != "" {
		return d.Dial(a.network, a.address)
	}
	for _, network := range [...]string{"unixgram", "unix"} {
		for _, path := range localSockets {
			if conn, err := d.Dial(network, path); err == nil {
				return conn, nil
			}
		}
	}
	return nil, errNoLocalSyslog
}

// severity maps a log level to its syslog severity.
func severity(level types.LogLevel) syslog.Priority {
	switch level {
	case types.TraceLevel, types.DebugLevel:
		return syslog.LOG_DEBUG
	case types.WarnLevel:
		return syslog.LOG_WARNING
	case types.ErrorLevel:
		return syslog.LOG_ERR
	case types.FatalLevel, types.PanicLevel:
		return syslog.LOG_CRIT
	default:
		return syslog.LOG_INFO
	}
}

// Flush flushes the adapter (no-op for syslog)
func (a *SyslogAdapter) Flush() error {
	return nil
}

// Close closes the syslog connection; Write returns ErrSyslogClosed after it.
func (a *SyslogAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closed = true
	if a.conn == nil {
		return nil
	}
	err := a.conn.Close()
	a.conn = nil
	return err
}

// SetFormatter sets the formatter for this adapter (nil is ignored).
func (a *SyslogAdapter) SetFormatter(formatter types.Formatter) {
	if formatter == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.formatter = formatter
}

// SetFacility sets the syslog facility of the following lines.
func (a *SyslogAdapter) SetFacility(facility syslog.Priority) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.facility = facility & facilityMask
}

// GetFacility returns the current syslog facility
func (a *SyslogAdapter) GetFacility() syslog.Priority {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.facility
}

// SetTag sets the syslog tag of the following lines.
func (a *SyslogAdapter) SetTag(tag string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tag = tag
}

// GetTag returns the current syslog tag
func (a *SyslogAdapter) GetTag() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tag
}

// Health returns nil while connected and, otherwise, why the adapter is not.
func (a *SyslogAdapter) Health() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch {
	case a.conn != nil:
		return nil
	case a.closed:
		return ErrSyslogClosed
	case a.configErr != nil:
		return a.configErr
	case a.lastError != nil:
		return a.lastError
	}
	return ErrSyslogNotInit
}

// WriteZero implements types.InternalAdapter; it is Write.
func (a *SyslogAdapter) WriteZero(entry *types.LogEntry) error {
	return a.Write(entry)
}
