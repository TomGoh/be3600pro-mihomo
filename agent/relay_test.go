package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRelayPlannerResolvesSharedHostOnceAndKeepsExactPorts(t *testing.T) {
	root, run := t.TempDir(), t.TempDir()
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"proxies": []any{map[string]any{"server": "relay.invalid", "port": 9044}, map[string]any{"server": "relay.invalid", "port": 9443}, map[string]any{"server": "192.0.2.10", "port": 9044}}})
	writeTestJSON(t, filepath.Join(root, "state.json"), State{})
	var calls atomic.Int32
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		calls.Add(1)
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}, {IP: net.ParseIP("2001:db8::10")}}, nil
	}
	if err := WriteRelayBypass(context.Background(), root, run, resolve); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(run, "relay-endpoints.ipset"))
	s := string(raw)
	if calls.Load() != 1 {
		t.Fatal("same hostname resolved repeatedly")
	}
	for _, entry := range []string{"192.0.2.10,tcp:9044", "192.0.2.10,udp:9443", "2001:db8::10,tcp:9044", "family inet6"} {
		if !strings.Contains(s, entry) {
			t.Fatal("missing relay entry", entry)
		}
	}
	if strings.Contains(s, "tcp:443\n") || strings.Count(s, "add CDX_RLY4 192.0.2.10,tcp:9044\n") != 1 {
		t.Fatal("port scope widened or duplicates retained")
	}
}

func TestRelayPlannerIncludesManagedSourcesAndBoundsFailedDNS(t *testing.T) {
	root, run := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(run, "providers"), 0700)
	file := filepath.Join(run, "providers", "sub-0123456789ab-1.json")
	writeTestJSON(t, file, map[string]any{"proxies": []any{map[string]any{"server": "198.51.100.20", "port": 4433}}})
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"proxies": []any{map[string]any{"server": "unresolved.invalid", "port": 443}}})
	writeTestJSON(t, filepath.Join(root, "state.json"), State{Subscriptions: []Subscription{{ID: "sub-0123456789ab", Path: file}}})
	if err := WriteRelayBypass(context.Background(), root, run, func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("fixture DNS unavailable") }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(run, "relay-endpoints.ipset"))
	if !strings.Contains(string(raw), "198.51.100.20,tcp:4433") {
		t.Fatal("managed relay omitted")
	}
}
