//go:build !windows

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

package syslog

import (
	"bufio"
	"errors"
	"fmt"
	"log/syslog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
)

func udpCollector(t *testing.T) (*net.UDPConn, string) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, conn.LocalAddr().String()
}

func readDatagram(t *testing.T, conn *net.UDPConn) string {
	t.Helper()
	buf := make([]byte, 64<<10)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no datagram: %v", err)
	}
	return string(buf[:n])
}

// tcpCollector accepts connections and sends each newline-framed record it
// reads on the returned channel.
func tcpCollector(t *testing.T, l net.Listener) <-chan string {
	t.Helper()
	records := make(chan string, 1024)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					records <- sc.Text()
				}
			}()
		}
	}()
	return records
}

func entry(msg string) *types.LogEntry {
	return &types.LogEntry{Level: types.InfoLevel, Message: msg}
}

// A network record keeps the log/syslog wire format.
func TestSyslogAdapter_WireFormat(t *testing.T) {
	conn, addr := udpCollector(t)
	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "udp", Address: addr, Facility: syslog.LOG_LOCAL0, Tag: "app"})
	defer func() { _ = a.Close() }()

	if err := a.Write(entry("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	host, _ := os.Hostname()
	want := regexp.MustCompile(fmt.Sprintf(`^<134>\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\S* %s app\[%d\]: .*hello.*\n$`,
		regexp.QuoteMeta(host), os.Getpid()))
	if got := readDatagram(t, conn); !want.MatchString(got) {
		t.Fatalf("record %q does not match %s", got, want)
	}
}

// SetTag and SetFacility apply to the next line; a zero facility is LOCAL0,
// never the kernel's.
func TestSyslogAdapter_FacilityAndTag(t *testing.T) {
	conn, addr := udpCollector(t)
	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "udp", Address: addr, Tag: "old"})
	defer func() { _ = a.Close() }()

	_ = a.Write(entry("first"))
	if got := readDatagram(t, conn); !strings.HasPrefix(got, "<134>") {
		t.Fatalf("zero facility: record %q, want local0.info <134>", got)
	}
	a.SetTag("new")
	a.SetFacility(syslog.LOG_LOCAL5)
	_ = a.Write(entry("second"))
	if got := readDatagram(t, conn); !strings.HasPrefix(got, "<174>") || !strings.Contains(got, " new[") {
		t.Fatalf("after SetFacility(LOCAL5) and SetTag(new): record %q", got)
	}
}

// A newline in a message cannot split it into a second, forged record.
func TestSyslogAdapter_NewlineCannotForgeARecord(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	records := tcpCollector(t, l)
	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "tcp", Address: l.Addr().String(), Tag: "app"})

	_ = a.Write(entry("login failed\n<134>Jan  1 00:00:00 host forged[1]: admin granted"))
	_ = a.Write(entry("next"))
	_ = a.Close()

	first, second := <-records, <-records
	if !strings.Contains(first, `login failed\n<134>`) || !strings.Contains(second, "next") {
		t.Fatalf("records %q, %q: want the newline escaped inside the first", first, second)
	}
}

// A collector that is down when the adapter starts is reached once it comes
// up.
func TestSyslogAdapter_ReconnectsWhenCollectorStartsLate(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "tcp", Address: addr, Tag: "app"})
	defer func() { _ = a.Close() }()
	if !errors.Is(a.Health(), ErrSyslogNotInit) {
		t.Fatalf("Health with the collector down = %v", a.Health())
	}

	if l, err = net.Listen("tcp", addr); err != nil {
		t.Skip("could not reuse the port:", err)
	}
	defer func() { _ = l.Close() }()
	records := tcpCollector(t, l)
	deadline := time.Now().Add(5 * time.Second)
	for a.Write(entry("up")) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("still failing 5s after the collector came up: %v", a.Health())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := <-records; !strings.Contains(got, "up") {
		t.Fatalf("record %q", got)
	}
}

// A collector that stops reading cannot block Write or Close beyond the
// write timeout, over TCP or a local datagram socket.
func TestSyslogAdapter_StalledCollectorDoesNotBlock(t *testing.T) {
	stalled := map[string]func(t *testing.T) string{
		"tcp": func(t *testing.T) string {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = l.Close() })
			go func() { // accept, never read
				var held []net.Conn
				for {
					conn, err := l.Accept()
					if err != nil {
						for _, c := range held {
							_ = c.Close()
						}
						return
					}
					held = append(held, conn)
				}
			}()
			return l.Addr().String()
		},
		"unixgram": func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "log.sock")
			conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
			if err != nil {
				t.Skip("unixgram unavailable:", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			return path
		},
	}
	for network, listen := range stalled {
		t.Run(network, func(t *testing.T) {
			a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: network, Address: listen(t), Tag: "app"})
			a.writeTimeout = 100 * time.Millisecond
			big := entry(strings.Repeat("x", 1024))

			start := time.Now()
			var err error
			for i := 0; err == nil && i < 100_000; i++ {
				err = a.Write(big)
			}
			if !errors.Is(err, ErrSyslogWrite) {
				t.Fatalf("100000 writes to a stalled collector never failed: %v", err)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("writes blocked for %v", took)
			}
			if err := a.Write(big); err == nil {
				t.Fatal("Write during the reconnection backoff succeeded")
			}
			done := make(chan struct{})
			go func() { _ = a.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Close blocked")
			}
		})
	}
}

// Setting only one of Network and Address is an error, not a silent switch
// to the local socket.
func TestSyslogAdapter_PartialAddressIsAnError(t *testing.T) {
	for _, opts := range []SyslogAdapterOptions{{Network: "udp"}, {Address: "127.0.0.1:514"}} {
		a := NewSyslogAdapterWithOptions(&opts)
		if err := a.Write(entry("x")); !errors.Is(err, ErrSyslogAddress) {
			t.Errorf("%+v: Write = %v, want ErrSyslogAddress", opts, err)
		}
		if err := a.Health(); !errors.Is(err, ErrSyslogAddress) {
			t.Errorf("%+v: Health = %v, want ErrSyslogAddress", opts, err)
		}
		_ = a.Close()
	}
}

// Nil options and nil entries are refused without a panic, and so are lines
// written after Close.
func TestSyslogAdapter_RefusesNilAndClosed(t *testing.T) {
	_, addr := udpCollector(t)
	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "udp", Address: addr})
	if err := a.Write(nil); !errors.Is(err, ErrSyslogNilEntry) {
		t.Fatalf("Write(nil) = %v", err)
	}
	_ = a.Close()
	if err := a.Write(entry("x")); !errors.Is(err, ErrSyslogClosed) {
		t.Fatalf("Write after Close = %v", err)
	}
	_ = NewSyslogAdapterWithOptions(nil).Close()
}

// Writes, setters, and Close race without a data race.
func TestSyslogAdapter_ConcurrentUseAndClose(t *testing.T) {
	_, addr := udpCollector(t)
	a := NewSyslogAdapterWithOptions(&SyslogAdapterOptions{Network: "udp", Address: addr, Tag: "app"})

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				_ = a.Write(entry(fmt.Sprintf("line-%d-%d", g, i)))
				if i%20 == 0 {
					a.SetTag("app")
					a.SetFacility(syslog.LOG_LOCAL1)
					_ = a.Health()
				}
			}
		}()
	}
	time.Sleep(time.Millisecond)
	_ = a.Close()
	wg.Wait()
}
