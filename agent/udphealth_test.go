package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
)

func TestUDPHealthThroughLoopbackSOCKSAndRejectsWrongTransaction(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			tcp, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer tcp.Close()
			udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()
			done := make(chan error, 1)
			go func() {
				conn, e := tcp.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close()
				request := make([]byte, 4)
				_, e = io.ReadFull(conn, request)
				if e == nil {
					_, e = conn.Write([]byte{5, 0})
				}
				associate := make([]byte, 10)
				if e == nil {
					_, e = io.ReadFull(conn, associate)
				}
				response := []byte{5, 0, 0, 1, 127, 0, 0, 1}
				response = binary.BigEndian.AppendUint16(response, uint16(udp.LocalAddr().(*net.UDPAddr).Port))
				if e == nil {
					_, e = conn.Write(response)
				}
				packet := make([]byte, 1500)
				if e != nil {
					done <- e
					return
				}
				n, peer, e := udp.ReadFromUDP(packet)
				if e != nil {
					done <- e
					return
				}
				reader := bytes.NewReader(packet[4:n])
				_, e = readSocksAddress(reader, packet[3])
				if e != nil {
					done <- e
					return
				}
				offset := n - reader.Len()
				packet[offset] = 1
				packet[offset+1] = 1
				if wrong {
					packet[offset+8] ^= 1
				}
				_, e = udp.WriteToUDP(packet[:n], peer)
				done <- e
			}()
			got := probeUDP(context.Background(), tcp.Addr().String(), "", "", net.ParseIP("192.0.2.1"), 19302)
			want := "ok"
			if wrong {
				want = "failed"
			}
			if got != want {
				t.Fatalf("probe=%s want%s", got, want)
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}
