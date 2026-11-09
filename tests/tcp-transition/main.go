// Bounded local-network test peer. No Internet downloads or router configuration.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	server := flag.Bool("server", false, "serve echo on specified documentation addresses")
	address := flag.String("address", "198.51.100.77:18080", "probe address")
	seconds := flag.Int("seconds", 16, "bounded runtime, 1..90 seconds")
	flag.Parse()
	if *seconds < 1 || *seconds > 90 {
		os.Exit(2)
	}
	if *server {
		listeners := []net.Listener{}
		for _, a := range []string{"198.51.100.77:18080", "[2001:db8:cd00::77]:18080"} {
			l, e := net.Listen("tcp", a)
			if e != nil {
				fmt.Fprintln(os.Stderr, e)
				os.Exit(1)
			}
			listeners = append(listeners, l)
			go func() {
				for {
					c, e := l.Accept()
					if e != nil {
						return
					}
					go func(c net.Conn) {
						defer c.Close()
						_ = c.SetDeadline(time.Now().Add(90 * time.Second))
						_, _ = io.Copy(c, c)
					}(c)
				}
			}()
		}
		fmt.Println("TCP_ECHO_READY")
		time.Sleep(time.Duration(*seconds) * time.Second)
		for _, l := range listeners {
			_ = l.Close()
		}
		return
	}
	start := time.Now()
	c, e := net.DialTimeout("tcp", *address, 3*time.Second)
	count := 0
	failure := ""
	if e == nil {
		fmt.Println("TCP_PROBE_CONNECTED")
		defer c.Close()
		for time.Since(start) < time.Duration(*seconds)*time.Second {
			payload := bytes.Repeat([]byte{0x5a}, 128)
			binary.BigEndian.PutUint64(payload, uint64(count))
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			if _, e = c.Write(payload); e != nil {
				break
			}
			reply := make([]byte, len(payload))
			if _, e = io.ReadFull(c, reply); e != nil {
				break
			}
			if !bytes.Equal(reply, payload) {
				e = fmt.Errorf("echo content mismatch")
				break
			}
			count++
			time.Sleep(100 * time.Millisecond)
		}
	}
	if e != nil {
		failure = e.Error()
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"address": *address, "ok": e == nil, "exchanges": count, "elapsedSeconds": time.Since(start).Seconds(), "error": failure})
	if e != nil {
		os.Exit(1)
	}
}
