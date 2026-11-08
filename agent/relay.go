package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WriteRelayBypass plans exact upstream address/port pairs. It prevents a LAN
// client running its own proxy from tunnelling its relay connection through
// the same relay again. It does not exempt any client device or whole address.
func WriteRelayBypass(ctx context.Context, root, run string, resolve func(context.Context, string) ([]net.IPAddr, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if resolve == nil {
		resolve = net.DefaultResolver.LookupIPAddr
	}
	cfg, state, err := (&Store{Root: root}).Snapshot()
	if err != nil {
		return err
	}
	nodes, _ := cfg["proxies"].([]any)
	for _, sub := range state.Subscriptions {
		if !subscriptionID.MatchString(sub.ID) || filepath.Clean(filepath.Dir(sub.Path)) != filepath.Join(run, "providers") || !strings.HasPrefix(filepath.Base(sub.Path), sub.ID+"-") {
			return fmt.Errorf("invalid managed relay cache path")
		}
		data, e := os.ReadFile(sub.Path)
		if e != nil {
			return e
		}
		if len(data) > 4*1024*1024 {
			return fmt.Errorf("relay cache too large")
		}
		var value struct {
			Proxies []any `json:"proxies"`
		}
		if e = json.Unmarshal(data, &value); e != nil {
			return e
		}
		nodes = append(nodes, value.Proxies...)
	}
	hosts := map[string]map[uint16]bool{}
	for _, raw := range nodes {
		n, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		host, ok := n["server"].(string)
		if !ok || host == "" {
			continue
		}
		port, e := strconv.ParseUint(fmt.Sprint(n["port"]), 10, 16)
		if e != nil || port == 0 {
			continue
		}
		host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
		if hosts[host] == nil {
			hosts[host] = map[uint16]bool{}
		}
		hosts[host][uint16(port)] = true
	}
	type endpoint struct {
		family int
		ip     string
		port   uint16
	}
	endpoints := map[endpoint]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan string)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for host := range jobs {
				var addresses []net.IPAddr
				if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
					addresses = []net.IPAddr{{IP: ip}}
				} else {
					addresses, _ = resolve(ctx, host)
				}
				mu.Lock()
				for _, a := range addresses {
					if a.IP == nil || a.Zone != "" || a.IP.IsUnspecified() || a.IP.IsMulticast() {
						continue
					}
					family := 6
					if a.IP.To4() != nil {
						family = 4
					}
					for port := range hosts[host] {
						endpoints[endpoint{family, a.IP.String(), port}] = true
					}
				}
				mu.Unlock()
			}
		}()
	}
	for host := range hosts {
		jobs <- host
	}
	close(jobs)
	wg.Wait()
	if len(endpoints) > 8192 {
		return fmt.Errorf("too many resolved relay endpoints")
	}
	entries := make([]string, 0, len(endpoints)*2+4)
	for _, f := range []int{4, 6} {
		family := "inet"
		if f == 6 {
			family = "inet6"
		}
		entries = append(entries, fmt.Sprintf("create CDX_RLY%d hash:ip,port family %s hashsize 128 maxelem 16384", f, family), fmt.Sprintf("flush CDX_RLY%d", f))
	}
	adds := []string{}
	for e := range endpoints {
		for _, proto := range []string{"tcp", "udp"} {
			adds = append(adds, fmt.Sprintf("add CDX_RLY%d %s,%s:%d", e.family, e.ip, proto, e.port))
		}
	}
	sort.Strings(adds)
	entries = append(entries, adds...)
	if err = os.MkdirAll(run, 0700); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(run, "relay-endpoints.ipset"), []byte(strings.Join(entries, "\n")+"\n"))
}
