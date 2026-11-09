package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	if strings.Contains(s, "tcp:443\n") || strings.Count(s, "add CDX_RLY4 192.0.2.10,tcp:9044 timeout ") != 1 {
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

func TestRelayCacheExpiresPerFamilyAndRemovedHostsDisappear(t *testing.T) {
	root, run := t.TempDir(), t.TempDir()
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"proxies": []any{map[string]any{"server": "relay.invalid", "port": 9044}}})
	writeTestJSON(t, filepath.Join(root, "state.json"), State{})
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}, {IP: net.ParseIP("2001:db8::10")}}, nil
	}
	if err := writeRelayBypassAt(context.Background(), root, run, resolve, 100); err != nil {
		t.Fatal(err)
	}
	resolve = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.20")}}, nil
	}
	if err := writeRelayBypassAt(context.Background(), root, run, resolve, 200); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(run, "relay-endpoints.ipset"))
	if !strings.Contains(string(raw), "maxelem 16384 timeout 3600") || !strings.Contains(string(raw), "2001:db8::10,tcp:9044 timeout 3500") {
		t.Fatal("kernel entry TTL not bounded to cache age")
	}
	if !strings.Contains(string(raw), "192.0.2.20") || !strings.Contains(string(raw), "2001:db8::10") || strings.Contains(string(raw), "192.0.2.10") {
		t.Fatal("did not replace successful family and retain missing family")
	}
	fail := func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("DNS down") }
	if err := writeRelayBypassAt(context.Background(), root, run, fail, 3801); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(run, "relay-endpoints.ipset"))
	if strings.Contains(string(raw), "add ") {
		t.Fatal("expired DNS silently retained")
	}
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"proxies": []any{}})
	if err := writeRelayBypassAt(context.Background(), root, run, resolve, 3802); err != nil {
		t.Fatal(err)
	}
	cache, _ := os.ReadFile(filepath.Join(run, "relay-cache.json"))
	if strings.Contains(string(cache), "relay.invalid") {
		t.Fatal("removed host retained")
	}
}

func TestRelayDeadlineStillPublishesExpiredCache(t *testing.T) {
	root, run := t.TempDir(), t.TempDir()
	nodes := []any{}
	for i := 0; i < 24; i++ {
		nodes = append(nodes, map[string]any{"server": fmt.Sprintf("relay%d.invalid", i), "port": 9044})
	}
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"proxies": nodes})
	writeTestJSON(t, filepath.Join(root, "state.json"), State{})
	ok := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
	}
	if err := writeRelayBypassAt(context.Background(), root, run, ok, 100); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	slow := func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		return nil, ctx.Err()
	}
	if err := writeRelayBypassAt(ctx, root, run, slow, 3801); err != nil {
		t.Fatalf("deadline prevented expiry publication: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(run, "relay-endpoints.ipset"))
	if strings.Contains(string(raw), "add ") {
		t.Fatal("expired cache retained after deadline")
	}
}
