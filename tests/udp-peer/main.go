package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

func validateReply(from, want netip.AddrPort, payload []byte, family string) error {
	if from != want {
		return fmt.Errorf("unexpected source tuple: %s", from)
	}
	if !bytes.Equal(payload, []byte("mihomo-nonlocal-"+family+"-proof")) {
		return errors.New("UDP payload was altered or delivered to the wrong family")
	}
	return nil
}
func matrixPayload(family string, size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte((i*31 + int(family[3]-'0')) % 251)
	}
	return p
}

var matrixSizes = []int{0, 1, 3, 64, 1200, 1400, 4096, 32768}

func validateMatrix(from, want netip.AddrPort, payload []byte, family string) error {
	if from != want {
		return errors.New("matrix source tuple mismatch")
	}
	allowed := false
	for _, size := range matrixSizes {
		if len(payload) == size {
			allowed = true
		}
	}
	if !allowed || !bytes.Equal(payload, matrixPayload(family, len(payload))) {
		return errors.New("matrix payload mismatch")
	}
	return nil
}
func runPeer(listen4, listen6, source4, source6 string, matrix bool) error {
	type receiver struct {
		family   string
		conn     *net.UDPConn
		expected netip.AddrPort
	}
	var receivers []receiver
	defer func() {
		for _, r := range receivers {
			r.conn.Close()
		}
	}()
	for _, tc := range []struct{ network, local, source string }{{"udp4", listen4, source4}, {"udp6", listen6, source6}} {
		local, err := netip.ParseAddrPort(tc.local)
		if err != nil {
			return err
		}
		source, err := netip.ParseAddrPort(tc.source)
		if err != nil {
			return err
		}
		conn, err := net.ListenUDP(tc.network, net.UDPAddrFromAddrPort(local))
		if err != nil {
			return err
		}
		receivers = append(receivers, receiver{tc.network, conn, source})
	}
	fmt.Println("UDP_PEER_READY")
	var group sync.WaitGroup
	failures := make(chan error, 2)
	for _, r := range receivers {
		group.Add(1)
		go func(r receiver) {
			defer group.Done()
			if err := r.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
				failures <- err
				return
			}
			buffer := make([]byte, 65535)
			seen := map[int]bool{}
			expected := 1
			if matrix {
				expected = len(matrixSizes)
			}
			for len(seen) < expected {
				n, from, err := r.conn.ReadFromUDPAddrPort(buffer)
				if err == nil {
					if matrix {
						err = validateMatrix(from, r.expected, buffer[:n], r.family)
					} else {
						err = validateReply(from, r.expected, buffer[:n], r.family)
					}
				}
				if err != nil {
					failures <- fmt.Errorf("%s receive: %w", r.family, err)
					return
				}
				seen[n] = true
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"family": r.family, "source": from.String(), "destination": r.conn.LocalAddr().String(), "bytes": n, "verified": true})
			}
		}(r)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	return nil
}
func main() {
	a := flag.String("listen4", "192.0.2.2:40000", "isolated peer IPv4 endpoint")
	b := flag.String("listen6", "[2001:db8:7::2]:40000", "isolated peer IPv6 endpoint")
	c := flag.String("source4", "192.0.2.6:27051", "expected nonlocal server IPv4 source")
	d := flag.String("source6", "[2001:db8:7::9]:27052", "expected nonlocal server IPv6 source")
	matrix := flag.Bool("matrix", false, "verify multiple datagram sizes including fragmentation")
	flag.Parse()
	if err := runPeer(*a, *b, *c, *d, *matrix); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
