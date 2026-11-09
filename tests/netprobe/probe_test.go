package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func httpOptions(url string) HTTPOptions {
	return HTTPOptions{URL: url, Duration: time.Second, Workers: 1, Requests: 3, MaxBytes: 1048576}
}

func TestHTTPKeepaliveReusesOneConnection(t *testing.T) {
	var connections atomic.Int32
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("payload")) }))
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	s.Start()
	defer s.Close()
	var rows []HTTPResult
	if e := RunHTTP(context.Background(), httpOptions(s.URL), func(r HTTPResult) { rows = append(rows, r) }); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 3 || connections.Load() != 1 || !rows[1].Reused || !rows[2].Reused {
		t.Fatalf("connection evidence: %+v, connections=%d", rows, connections.Load())
	}
	for _, r := range rows {
		if !r.OK || r.Bytes != 7 {
			t.Fatalf("bad transfer: %+v", r)
		}
	}
}

func TestHTTPDurationStopRequiresARealResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Duration = 80 * time.Millisecond
	o.Requests = 1
	var row HTTPResult
	if e := RunHTTP(context.Background(), o, func(r HTTPResult) { row = r }); e != nil {
		t.Fatal(e)
	}
	if row.StopReason != "duration" || row.Bytes != 1 || row.Seconds < .06 {
		t.Fatalf("not a measured planned stop: %+v", row)
	}
}

func TestHTTPNoResponseCannotPassAsPlannedStop(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Duration = 40 * time.Millisecond
	o.Requests = 1
	if e := RunHTTP(context.Background(), o, func(HTTPResult) {}); e == nil {
		t.Fatal("no response falsely passed")
	}
}

func TestHTTPBudgetIsSharedAcrossWorkers(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, 32768)) }))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Workers = 4
	o.MaxBytes = 4096
	var total int64
	var mu sync.Mutex
	if e := RunHTTP(context.Background(), o, func(r HTTPResult) { mu.Lock(); total += r.Bytes; mu.Unlock() }); e != nil {
		t.Fatal(e)
	}
	if total != 4096 {
		t.Fatalf("global byte cap violated: %d", total)
	}
}

func TestHTTPFailureAndShortBodyCannotPass(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "status", true: "short-body"}[short], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if short {
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("short"))
				} else {
					w.WriteHeader(500)
				}
			}))
			defer s.Close()
			o := httpOptions(s.URL)
			o.Requests = 1
			if e := RunHTTP(context.Background(), o, func(r HTTPResult) {
				if r.StopReason != "" {
					t.Error("real failure marked planned")
				}
			}); e == nil {
				t.Fatal("failure falsely passed")
			}
		})
	}
}

func TestHTTPRejectsUnsafeLimitsBeforeNetwork(t *testing.T) {
	var called atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer s.Close()
	o := httpOptions(s.URL)
	o.MaxBytes = 0
	if RunHTTP(context.Background(), o, func(HTTPResult) {}) == nil || called.Load() {
		t.Fatal("invalid configuration reached network")
	}
}

func TestUDPEchoAndCorruptionDetection(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "echo", true: "corrupt"}[corrupt], func(t *testing.T) {
			c, e := net.ListenPacket("udp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			go func() {
				b := make([]byte, 2048)
				for {
					n, a, e := c.ReadFrom(b)
					if e != nil {
						return
					}
					if corrupt {
						b[n-1] ^= 1
					}
					_, _ = c.WriteTo(b[:n], a)
				}
			}()
			var rows []UDPResult
			var mu sync.Mutex
			err := RunUDP(context.Background(), UDPOptions{Address: c.LocalAddr().String(), Network: "udp4", Duration: time.Second, Workers: 2, Packets: 5, PayloadBytes: 64, Timeout: 50 * time.Millisecond}, func(r UDPResult) { mu.Lock(); rows = append(rows, r); mu.Unlock() })
			if corrupt && err == nil {
				t.Fatal("corrupt echo falsely passed")
			}
			if !corrupt && err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("missing worker results: %d", len(rows))
			}
			for _, r := range rows {
				if r.Sent != 5 || (!corrupt && (r.Received != 5 || len(r.Missing) != 0)) || (corrupt && r.Corrupt != 5) {
					t.Fatalf("bad counts: %+v", r)
				}
			}
		})
	}
}

func TestUDPLossIsReportedBySequence(t *testing.T) {
	c, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	var row UDPResult
	err := RunUDP(context.Background(), UDPOptions{Address: c.LocalAddr().String(), Network: "udp4", Duration: time.Second, Workers: 1, Packets: 3, PayloadBytes: 64, Timeout: 10 * time.Millisecond}, func(r UDPResult) { row = r })
	if err == nil || row.Sent != 3 || row.Received != 0 || len(row.Missing) != 3 {
		t.Fatalf("lost packets falsely passed: %+v %v", row, err)
	}
	for i, v := range row.Missing {
		if i != v {
			t.Fatal("wrong missing sequence")
		}
	}
}

func TestUDPPacketBudgetCannotOverflow(t *testing.T) {
	called := false
	err := RunUDP(context.Background(), UDPOptions{Address: "127.0.0.1:9", Network: "udp4", Duration: 20 * time.Millisecond, Workers: 32, Packets: math.MaxInt, PayloadBytes: 64, Timeout: time.Millisecond}, func(UDPResult) { called = true })
	if err == nil || called {
		t.Fatal("overflowed packet limit reached a network worker")
	}
}

func TestHTTPVerifiesTLSAndAcceptsAnExplicitTestCA(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	defer s.Close()
	o := httpOptions(s.URL)
	o.Requests = 1
	if RunHTTP(context.Background(), o, func(HTTPResult) {}) == nil {
		t.Fatal("untrusted TLS server accepted")
	}
	o.RootCAs = x509.NewCertPool()
	o.RootCAs.AddCert(s.Certificate())
	if err := RunHTTP(context.Background(), o, func(HTTPResult) {}); err != nil {
		t.Fatal(err)
	}
}

func TestCLIRequiresExplicitNetworkOptIn(t *testing.T) {
	var called atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer s.Close()
	if code := runCLI(context.Background(), []string{"-url", s.URL}, io.Discard, io.Discard); code != 2 || called.Load() {
		t.Fatal("CLI contacted target without opt-in")
	}
}

func TestCLIEmitsRunEnvelopeForOfflineCompletenessCheck(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer s.Close()
	var output bytes.Buffer
	code := runCLI(context.Background(), []string{"-run-live", "-url", s.URL, "-requests", "1"}, &output, io.Discard)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output.String())
	}
	dec := json.NewDecoder(&output)
	var rows []map[string]any
	for {
		var row map[string]any
		if err := dec.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 3 || rows[0]["event"] != "run-start" || rows[2]["event"] != "run-end" || rows[2]["completed"] != true || rows[2]["exitCode"] != float64(0) {
		t.Fatalf("bad envelope: %+v", rows)
	}
}
