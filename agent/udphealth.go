package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Minimal bounded SOCKS5 UDP association + one RFC 8489 Binding transaction.
// This checks the mixed-port route; it does not prove a LAN TPROXY/game flow.
func probeUDP(ctx context.Context, proxy, user, password string, target net.IP, port uint16) string {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	tcp, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return "failed"
	}
	defer tcp.Close()
	deadline, _ := ctx.Deadline()
	_ = tcp.SetDeadline(deadline)
	if _, err = tcp.Write([]byte{5, 2, 0, 2}); err != nil {
		return "failed"
	}
	var pair [2]byte
	if _, err = io.ReadFull(tcp, pair[:]); err != nil || pair[0] != 5 {
		return "failed"
	}
	if pair[1] == 2 {
		if len(user) > 255 || len(password) > 255 {
			return "failed"
		}
		auth := []byte{1, byte(len(user))}
		auth = append(auth, []byte(user)...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, []byte(password)...)
		if _, err = tcp.Write(auth); err != nil {
			return "failed"
		}
		if _, err = io.ReadFull(tcp, pair[:]); err != nil || pair != [2]byte{1, 0} {
			return "failed"
		}
	} else if pair[1] != 0 {
		return "failed"
	}
	if _, err = tcp.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return "failed"
	}
	var head [4]byte
	if _, err = io.ReadFull(tcp, head[:]); err != nil || head[0] != 5 || head[1] != 0 || head[2] != 0 {
		return "failed"
	}
	address, err := readSocksAddress(tcp, head[3])
	if err != nil {
		return "failed"
	}
	host, p, e := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if e != nil || ip == nil || (!ip.IsUnspecified() && !ip.IsLoopback()) {
		return "failed"
	}
	host = "127.0.0.1"
	if ip.To4() == nil {
		host = "::1"
	}
	udp, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(host, p))
	if err != nil {
		return "failed"
	}
	defer udp.Close()
	_ = udp.SetDeadline(deadline)
	payload := make([]byte, 20)
	payload[1] = 1
	binary.BigEndian.PutUint32(payload[4:8], 0x2112a442)
	if _, err = rand.Read(payload[8:]); err != nil {
		return "failed"
	}
	packet := []byte{0, 0, 0}
	if v4 := target.To4(); v4 != nil {
		packet = append(packet, 1)
		packet = append(packet, v4...)
	} else if v6 := target.To16(); v6 != nil {
		packet = append(packet, 4)
		packet = append(packet, v6...)
	} else {
		return "failed"
	}
	packet = binary.BigEndian.AppendUint16(packet, port)
	packet = append(packet, payload...)
	if _, err = udp.Write(packet); err != nil {
		return "failed"
	}
	response := make([]byte, 1500)
	n, err := udp.Read(response)
	if err != nil || n < 4 || !bytes.Equal(response[:3], []byte{0, 0, 0}) {
		return "failed"
	}
	reader := bytes.NewReader(response[4:n])
	if _, err = readSocksAddress(reader, response[3]); err != nil {
		return "failed"
	}
	reply, _ := io.ReadAll(reader)
	if validSTUNReply(reply, payload[8:]) {
		return "ok"
	}
	return "failed"
}
func readSocksAddress(r io.Reader, kind byte) (string, error) {
	size := 0
	switch kind {
	case 1:
		size = 4
	case 4:
		size = 16
	default:
		return "", fmt.Errorf("unsupported SOCKS address")
	}
	raw := make([]byte, size+2)
	if _, err := io.ReadFull(r, raw); err != nil {
		return "", err
	}
	return net.JoinHostPort(net.IP(raw[:size]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(raw[size:])))), nil
}
func validSTUNReply(reply, transaction []byte) bool {
	return len(reply) >= 20 && len(transaction) == 12 && reply[0] == 1 && reply[1] == 1 && binary.BigEndian.Uint32(reply[4:8]) == 0x2112a442 && int(binary.BigEndian.Uint16(reply[2:4])) == len(reply)-20 && len(reply)%4 == 0 && bytes.Equal(reply[8:20], transaction)
}
