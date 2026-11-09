package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A completed sample followed by a request awaiting headers must keep the
// sample, while recording the measurement cutoff as a zero-sample worker stop.
func TestHTTPMeasurementDeadlineBeforeHeadersStopsWorker(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte("ok"))
			return
		}
		<-r.Context().Done()
	}))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Duration = 150 * time.Millisecond
	o.Requests = 2
	var rows []HTTPResult
	err := RunHTTP(context.Background(), o, func(r HTTPResult) { rows = append(rows, r) })
	if err != nil {
		t.Fatalf("measurement cutoff failed a run with response evidence: %v; %+v", err, rows)
	}
	if len(rows) != 2 || !rows[0].OK || rows[0].Bytes != 2 {
		t.Fatalf("missing completed evidence: %+v", rows)
	}
	stop := rows[1]
	if stop.Event != "worker-stop" || stop.StopReason != "duration" || stop.OK || stop.Status != 0 || stop.Bytes != 0 || stop.TTFBMilliseconds != 0 || stop.Error != "" || stop.SHA256 != "" {
		t.Fatalf("cutoff must be a zero-sample worker stop: %+v", stop)
	}
}

func TestHTTPMeasurementDeadlineWithoutAnyResponseRemainsFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Duration = 80 * time.Millisecond
	o.Requests = 1
	var rows []HTTPResult
	err := RunHTTP(context.Background(), o, func(r HTTPResult) { rows = append(rows, r) })
	if err == nil || len(rows) != 1 || rows[0].Event != "worker-stop" || rows[0].OK {
		t.Fatalf("missing response evidence must fail despite an orderly worker stop: %v %+v", err, rows)
	}
}

func TestHTTPEarlierFailuresSurviveMeasurementCutoff(t *testing.T) {
	for _, mode := range []string{"404", "500", "eof", "short-body"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch calls.Add(1) {
				case 1:
					if mode == "eof" {
						w.Header().Set("Connection", "close")
					}
					_, _ = w.Write([]byte("ok"))
				case 2:
					switch mode {
					case "404":
						w.WriteHeader(404)
					case "500":
						w.WriteHeader(500)
					case "eof":
						c, _, _ := w.(http.Hijacker).Hijack()
						_ = c.Close()
					case "short-body":
						w.Header().Set("Content-Length", "100")
						_, _ = w.Write([]byte("short"))
					}
				default:
					<-r.Context().Done()
				}
			}))
			defer s.Close()
			o := httpOptions(s.URL)
			o.Duration = 150 * time.Millisecond
			o.Requests = 3
			var rows []HTTPResult
			err := RunHTTP(context.Background(), o, func(r HTTPResult) { rows = append(rows, r) })
			if err == nil || len(rows) != 3 || !rows[0].OK || rows[1].Event != "request" || rows[1].OK || rows[1].StopReason != "" || rows[2].Event != "worker-stop" {
				t.Fatalf("real failure hidden by subsequent cutoff: %v %+v", err, rows)
			}
		})
	}
}

func TestHTTPParentCancellationAfterSampleIsNotMeasurementStop(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				parent, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
			defer cancel()
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					_, _ = w.Write([]byte("ok"))
					return
				}
				if !deadline {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer s.Close()
			o := httpOptions(s.URL)
			o.Duration = time.Second
			o.Requests = 2
			var rows []HTTPResult
			err := RunHTTP(parent, o, func(r HTTPResult) { rows = append(rows, r) })
			if err == nil || len(rows) != 2 || !rows[0].OK || rows[1].Event != "request" || rows[1].StopReason != "" || rows[1].OK {
				t.Fatalf("parent cancellation became an orderly measurement stop: %v %+v", err, rows)
			}
		})
	}
}

func TestHTTPRequestTimeoutAfterHeadersRemainsFailure(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Duration = 17 * time.Second
	o.Requests = 1
	var row HTTPResult
	err := RunHTTP(context.Background(), o, func(r HTTPResult) { row = r })
	if err == nil || row.Event != "request" || row.StopReason != "" || row.OK || row.Status != 200 || row.Bytes != 1 {
		t.Fatalf("per-request timeout became a measurement stop: %v %+v", err, row)
	}
}

// Even one response byte (including an incomplete header) makes this a real
// response attempt: it cannot be discarded as a pre-response worker stop.
func TestHTTPMeasurementDeadlineDoesNotHideIncompleteResponse(t *testing.T) {
	for _, mode := range []string{"partial-headers", "empty-body", "404-body", "500-body"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Connection", "close")
					_, _ = w.Write([]byte("ok"))
					return
				}
				if mode == "partial-headers" {
					conn, buffer, _ := w.(http.Hijacker).Hijack()
					defer conn.Close()
					_, _ = buffer.WriteString("HTTP/1.1 200")
					_ = buffer.Flush()
					<-release
					return
				}
				w.Header().Set("Content-Length", "10")
				switch mode {
				case "404-body":
					w.WriteHeader(404)
				case "500-body":
					w.WriteHeader(500)
				}
				if mode != "empty-body" {
					_, _ = w.Write([]byte("x"))
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer s.Close()
			defer close(release)
			o := httpOptions(s.URL)
			o.Duration = 150 * time.Millisecond
			o.Requests = 2
			var rows []HTTPResult
			err := RunHTTP(context.Background(), o, func(r HTTPResult) { rows = append(rows, r) })
			if err == nil || len(rows) != 2 || !rows[0].OK || rows[1].Event != "request" || rows[1].StopReason != "" || rows[1].OK {
				t.Fatalf("incomplete/failed response hidden as measurement stop: %v %+v", err, rows)
			}
		})
	}
}

func TestHTTPResponseHeaderTimeoutRemainsFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte("ok"))
			return
		}
		<-r.Context().Done()
	}))
	defer s.Close()
	o := httpOptions(s.URL)
	o.Duration = 14 * time.Second
	o.Requests = 2
	var rows []HTTPResult
	err := RunHTTP(context.Background(), o, func(r HTTPResult) { rows = append(rows, r) })
	if err == nil || len(rows) != 2 || !rows[0].OK || rows[1].Event != "request" || rows[1].StopReason != "" || rows[1].OK || rows[1].Status != 0 || rows[1].Bytes != 0 {
		t.Fatalf("response-header timeout became a measurement stop: %v %+v", err, rows)
	}
}
