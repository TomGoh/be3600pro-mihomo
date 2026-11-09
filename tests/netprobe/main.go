package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func runCLI(ctx context.Context, args []string, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("netprobe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	live := flags.Bool("run-live", false, "explicitly authorize bounded network traffic")
	mode := flags.String("mode", "http", "http, udp or udp-server")
	url := flags.String("url", "", "HTTP(S) target (no default target)")
	address := flags.String("address", "", "UDP target or explicit private/loopback server bind address")
	duration := flags.Duration("duration", 30*time.Second, "maximum 30 minutes")
	workers := flags.Int("workers", 1, "HTTP 1..8; UDP 1..32")
	requests := flags.Int("requests", 100, "per-worker HTTP request count cap")
	maxBytes := flags.Int64("max-bytes", 32*1024*1024, "global HTTP read / UDP server response byte cap; max 512 MiB")
	rate := flags.Int64("read-rate", 0, "application read bytes/sec per HTTP worker; not wire-rate shaping")
	interval := flags.Duration("interval", time.Second, "interval between requests/packets per worker")
	iface := flags.String("interface", "", "bind sockets to interface; Linux may require privileges")
	dns := flags.String("dns", "", "optional DNS server including port")
	family := flags.String("family", "", "tcp4/tcp6 or udp4/udp6")
	allowHTTP := flags.Bool("allow-http", false, "allow plain HTTP to a controlled non-loopback peer")
	stream := flags.Bool("stream", false, "allow a request to last for the full measurement duration")
	packets := flags.Int("packets", 20, "UDP packet count per worker; total <= 20000")
	payload := flags.Int("payload-bytes", 512, "UDP payload size, 34..1400")
	timeout := flags.Duration("udp-timeout", time.Second, "UDP reply timeout, <=10 seconds")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !*live || *duration < time.Second || *duration > 30*time.Minute {
		fmt.Fprintln(stderr, "Explicit -run-live and a duration of 1s..30m are required; no network operation was started.")
		return 2
	}
	if *mode != "http" && *mode != "udp" && *mode != "udp-server" {
		fmt.Fprintln(stderr, "Unknown mode")
		return 2
	}
	encoder := json.NewEncoder(out)
	started := time.Now()
	writeErr := false
	emit := func(v any) {
		if encoder.Encode(v) != nil {
			writeErr = true
		}
	}
	emit(map[string]any{"event": "run-start", "schema": 1, "mode": *mode, "workers": *workers, "durationLimitSeconds": duration.Seconds(), "byteLimit": *maxBytes, "unixTime": float64(time.Now().UnixMilli()) / 1000})
	var err error
	switch *mode {
	case "http":
		err = RunHTTP(ctx, HTTPOptions{URL: *url, Duration: *duration, Workers: *workers, Requests: *requests, MaxBytes: *maxBytes, ReadRate: *rate, Interval: *interval, Interface: *iface, DNSAddress: *dns, Network: *family, AllowHTTP: *allowHTTP, Stream: *stream}, func(r HTTPResult) { emit(r) })
	case "udp":
		network := *family
		if network == "" {
			network = "udp4"
		}
		err = RunUDP(ctx, UDPOptions{Address: *address, Network: network, Duration: *duration, Timeout: *timeout, Interval: *interval, Workers: *workers, Packets: *packets, PayloadBytes: *payload, Interface: *iface}, func(r UDPResult) { emit(r) })
	case "udp-server":
		err = serveUDP(ctx, *address, *duration, *maxBytes, func(n int64) { emit(map[string]any{"event": "udp-server", "bytes": n}) })
	}
	code := 0
	if err != nil || writeErr {
		code = 1
	}
	emit(map[string]any{"event": "run-end", "completed": err == nil && !writeErr, "exitCode": code, "elapsedSeconds": time.Since(started).Seconds()})
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	if writeErr {
		return 1
	}
	return code
}

func serveUDP(parent context.Context, address string, duration time.Duration, maxBytes int64, done func(int64)) error {
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return errors.New("explicit UDP server address required")
	}
	ip := net.ParseIP(host)
	documentation := ip != nil && (inPrefix(ip, "198.51.100.0/24") || inPrefix(ip, "2001:db8::/32"))
	if ip == nil || ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate() || documentation) || maxBytes < 1 || maxBytes > 512*1024*1024 {
		return errors.New("server bind must be an explicit private, loopback or documentation address with a bounded byte cap")
	}
	conn, e := net.ListenPacket("udp", address)
	if e != nil {
		return e
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	buffer := make([]byte, 2048)
	var sent int64
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, from, e := conn.ReadFrom(buffer)
		if e != nil {
			if timeout, ok := e.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return e
		}
		if n < 34 || n > 1400 || !bytes.HasPrefix(buffer[:n], udpMagic) {
			continue
		}
		if sent+int64(n) > maxBytes {
			break
		}
		count, e := conn.WriteTo(buffer[:n], from)
		if e != nil {
			return e
		}
		sent += int64(count)
	}
	done(sent)
	if parent.Err() != nil {
		return parent.Err()
	}
	return nil
}

func inPrefix(ip net.IP, prefix string) bool {
	_, network, _ := net.ParseCIDR(prefix)
	return network.Contains(ip)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
