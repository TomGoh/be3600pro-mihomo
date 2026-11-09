package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var udpMagic = []byte("cdx-udp-v1")

type UDPOptions struct {
	Address, Network, Interface    string
	Duration, Timeout, Interval    time.Duration
	Workers, Packets, PayloadBytes int
}
type UDPResult struct {
	Event           string  `json:"event"`
	Worker          int     `json:"worker"`
	Nonce           string  `json:"nonce"`
	Sent            int     `json:"sent"`
	Received        int     `json:"received"`
	Missing         []int   `json:"missing"`
	Corrupt         int     `json:"corrupt"`
	Seconds         float64 `json:"seconds"`
	Error           string  `json:"error,omitempty"`
	ExpectedPackets int     `json:"expectedPackets"`
}

func RunUDP(parent context.Context, o UDPOptions, emit func(UDPResult)) error {
	if (o.Network != "udp4" && o.Network != "udp6") || o.Duration <= 0 || o.Duration > 30*time.Minute || o.Timeout <= 0 || o.Timeout > 10*time.Second || o.Interval < 0 || o.Workers < 1 || o.Workers > 32 || o.Packets < 1 || o.Packets > 20000/o.Workers || o.PayloadBytes < 34 || o.PayloadBytes > 1400 {
		return errors.New("invalid UDP bounds")
	}
	address, e := net.ResolveUDPAddr(o.Network, o.Address)
	if e != nil {
		return e
	}
	control, e := interfaceControl(o.Interface)
	if e != nil {
		return e
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, o.Duration)
	defer cancel()
	var wg sync.WaitGroup
	var outputMu sync.Mutex
	var failed atomic.Bool
	for worker := 0; worker < o.Workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			started := time.Now()
			r := UDPResult{Event: "udp", Worker: worker, Nonce: hex.EncodeToString(nonce), Missing: []int{}, ExpectedPackets: o.Packets}
			dialer := net.Dialer{Timeout: o.Timeout, Control: control}
			conn, err := dialer.DialContext(ctx, o.Network, address.String())
			if err == nil {
				defer conn.Close()
				for seq := 0; seq < o.Packets && ctx.Err() == nil; seq++ {
					begin := time.Now()
					packet := make([]byte, o.PayloadBytes)
					copy(packet, udpMagic)
					binary.BigEndian.PutUint32(packet[10:], uint32(worker))
					binary.BigEndian.PutUint32(packet[14:], uint32(seq))
					copy(packet[18:], nonce)
					deadline := time.Now().Add(o.Timeout)
					if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
						deadline = end
					}
					_ = conn.SetDeadline(deadline)
					if _, err = conn.Write(packet); err != nil {
						break
					}
					r.Sent++
					buffer := make([]byte, 2048)
					n, readErr := conn.Read(buffer)
					if readErr != nil {
						r.Missing = append(r.Missing, seq)
					} else if !bytes.Equal(packet, buffer[:n]) {
						r.Corrupt++
					} else {
						r.Received++
					}
					delay := o.Interval - time.Since(begin)
					if delay > 0 {
						select {
						case <-time.After(delay):
						case <-ctx.Done():
						}
					}
				}
			}
			if err != nil {
				r.Error = err.Error()
			}
			r.Seconds = time.Since(started).Seconds()
			if r.Sent != o.Packets || r.Received != r.Sent || r.Corrupt != 0 || r.Error != "" {
				failed.Store(true)
			}
			outputMu.Lock()
			emit(r)
			outputMu.Unlock()
		}(worker)
	}
	wg.Wait()
	if failed.Load() {
		return errors.New("UDP loss, corruption or incomplete worker")
	}
	return nil
}
