package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type HTTPOptions struct {
	URL                            string
	Duration                       time.Duration
	Workers, Requests              int
	Interval                       time.Duration
	MaxBytes, ReadRate             int64
	Interface, DNSAddress, Network string
	AllowHTTP, Stream              bool
	RootCAs                        *x509.CertPool
}

type HTTPResult struct {
	Event            string  `json:"event"`
	Worker           int     `json:"worker"`
	Status           int     `json:"status"`
	Bytes            int64   `json:"bytes"`
	Seconds          float64 `json:"seconds"`
	UnixTime         float64 `json:"unixTime"`
	TTFBMilliseconds float64 `json:"ttfbMs"`
	Reused           bool    `json:"reused"`
	SHA256           string  `json:"sha256,omitempty"`
	OK               bool    `json:"ok"`
	Error            string  `json:"error,omitempty"`
	StopReason       string  `json:"stopReason,omitempty"`
}

type byteBudget struct {
	sync.Mutex
	remaining int64
}

func (b *byteBudget) reserve(n int) int {
	b.Lock()
	defer b.Unlock()
	if int64(n) > b.remaining {
		n = int(b.remaining)
	}
	b.remaining -= int64(n)
	return n
}
func (b *byteBudget) refund(n int) { b.Lock(); b.remaining += int64(n); b.Unlock() }

func RunHTTP(parent context.Context, o HTTPOptions, emit func(HTTPResult)) error {
	u, e := url.Parse(o.URL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("a target URL without credentials or fragment is required")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (o.AllowHTTP || ip != nil && ip.IsLoopback())) {
		return errors.New("HTTPS required; allow HTTP explicitly for a controlled peer")
	}
	if o.Duration <= 0 || o.Duration > 30*time.Minute || o.Workers < 1 || o.Workers > 8 || o.Requests < 1 || o.Requests > 1000 || o.MaxBytes < 1 || o.MaxBytes > 512*1024*1024 || o.ReadRate < 0 || o.ReadRate > 2*1024*1024 || o.Interval < 0 {
		return errors.New("invalid duration, concurrency, request count, byte budget or rate")
	}
	if o.Network != "" && o.Network != "tcp" && o.Network != "tcp4" && o.Network != "tcp6" {
		return errors.New("HTTP family must be tcp, tcp4 or tcp6")
	}
	control, e := interfaceControl(o.Interface)
	if e != nil {
		return e
	}
	if o.DNSAddress != "" {
		if _, _, e := net.SplitHostPort(o.DNSAddress); e != nil {
			return errors.New("DNS address must include a port")
		}
	}
	dnsDialer := net.Dialer{Timeout: 4 * time.Second, Control: control}
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if o.DNSAddress != "" {
			address = o.DNSAddress
		}
		return dnsDialer.DialContext(ctx, network, address)
	}}
	dialer := net.Dialer{Timeout: 6 * time.Second, Control: control, Resolver: resolver, KeepAlive: 15 * time.Second}
	ctx, cancel := context.WithTimeout(parent, o.Duration)
	defer cancel()
	budget := &byteBudget{remaining: o.MaxBytes}
	var failed, observed atomic.Bool
	var wg sync.WaitGroup
	var emitMu sync.Mutex
	output := func(r HTTPResult) { emitMu.Lock(); defer emitMu.Unlock(); emit(r) }
	for worker := 0; worker < o.Workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if o.Network != "" {
					network = o.Network
				}
				return dialer.DialContext(ctx, network, address)
			}, TLSClientConfig: &tls.Config{RootCAs: o.RootCAs, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: false, MaxIdleConnsPerHost: 1, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 12 * time.Second}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			for attempt := 0; attempt < o.Requests && ctx.Err() == nil; attempt++ {
				budget.Lock()
				empty := budget.remaining == 0
				budget.Unlock()
				if empty {
					return
				}
				started := time.Now()
				r := HTTPResult{Event: "request", Worker: worker}
				var traceMu sync.Mutex
				var reused bool
				var first float64
				trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { traceMu.Lock(); reused = i.Reused; traceMu.Unlock() }, GotFirstResponseByte: func() { traceMu.Lock(); first = float64(time.Since(started).Microseconds()) / 1000; traceMu.Unlock() }}
				requestCtx := ctx
				stop := func() {}
				if !o.Stream {
					requestCtx, stop = context.WithTimeout(ctx, 15*time.Second)
				}
				req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(requestCtx, trace), http.MethodGet, o.URL, nil)
				req.Header.Set("User-Agent", "be3600pro-mihomo-diagnostic/1")
				response, err := client.Do(req)
				digest := sha256.New()
				if err == nil {
					r.Status = response.StatusCode
					buffer := make([]byte, 32768)
					lastProgress := started
					for {
						allowance := budget.reserve(len(buffer))
						if allowance == 0 {
							r.StopReason = "budget"
							break
						}
						n, readErr := response.Body.Read(buffer[:allowance])
						budget.refund(allowance - n)
						r.Bytes += int64(n)
						_, _ = digest.Write(buffer[:n])
						if o.ReadRate > 0 && n > 0 {
							delay := time.Duration(float64(r.Bytes)/float64(o.ReadRate)*float64(time.Second)) - time.Since(started)
							if delay > 0 {
								select {
								case <-time.After(delay):
								case <-ctx.Done():
									readErr = ctx.Err()
								}
							}
						}
						if o.Stream && time.Since(lastProgress) >= 2*time.Second {
							progress := r
							progress.Event = "progress"
							progress.Seconds = time.Since(started).Seconds()
							progress.UnixTime = float64(time.Now().UnixMilli()) / 1000
							output(progress)
							lastProgress = time.Now()
						}
						if readErr != nil {
							if readErr != io.EOF {
								err = readErr
							}
							break
						}
						if response.ContentLength >= 0 && r.Bytes == response.ContentLength {
							break
						}
					}
					_ = response.Body.Close()
				}
				stop()
				r.Seconds = time.Since(started).Seconds()
				r.UnixTime = float64(time.Now().UnixMilli()) / 1000
				r.SHA256 = fmt.Sprintf("%x", digest.Sum(nil))
				traceMu.Lock()
				r.Reused = reused
				r.TTFBMilliseconds = first
				traceMu.Unlock()
				allowed := r.Status == 200 || r.Status == 204 || r.Status == 206
				if err != nil {
					r.Error = err.Error()
					if errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil && allowed && r.Bytes > 0 {
						r.StopReason = "duration"
					}
				}
				if !allowed {
					r.StopReason = ""
				}
				r.OK = err == nil && allowed && r.StopReason == ""
				planned := allowed && r.Bytes > 0 && (r.StopReason == "duration" || r.StopReason == "budget")
				idleBudget := allowed && err == nil && r.Bytes == 0 && r.StopReason == "budget"
				if idleBudget {
					r.Event = "worker-stop"
				}
				if r.OK || planned {
					observed.Store(true)
				} else if !idleBudget {
					failed.Store(true)
				}
				output(r)
				if r.StopReason != "" {
					return
				}
				select {
				case <-time.After(o.Interval):
				case <-ctx.Done():
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if failed.Load() || !observed.Load() || parent.Err() != nil {
		return errors.New("HTTP probe failed or produced no response evidence")
	}
	return nil
}
