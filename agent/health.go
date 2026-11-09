package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type healthSnapshot struct {
	ProxyUDP4        string         `json:"proxyUdp4"`
	ProxyUDP6        string         `json:"proxyUdp6"`
	CheckedAt        int64          `json:"checkedAt"`
	NetworkCheckedAt int64          `json:"networkCheckedAt"`
	Interception     map[string]any `json:"interception"`
	DNS              string         `json:"dns"`
	DirectHTTP       string         `json:"directHttp"`
	ProxyHTTP        string         `json:"proxyHttp"`
	UDP              string         `json:"udp"`
}
type healthState struct {
	sync.RWMutex
	value healthSnapshot
}

func (s *Server) healthView() map[string]any {
	s.health.RLock()
	v := s.health.value
	s.health.RUnlock()
	stale := v.CheckedAt == 0 || time.Now().Unix()-v.CheckedAt > 90
	if expected, ok := v.Interception["expectedEnabled"].(bool); ok && expected != s.options.Service.Enabled() {
		stale = true
	}
	return map[string]any{"snapshot": v, "stale": stale, "networkStale": v.NetworkCheckedAt == 0 || time.Now().Unix()-v.NetworkCheckedAt > 660}
}
func probeHTTP(ctx context.Context, client *http.Client, target string, expected int) string {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return "failed"
	}
	response, err := client.Do(req)
	if err != nil {
		return "failed"
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		return "failed"
	}
	// Bounded body and no redirects, credentials stay on the local proxy only.
	if _, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096)); err != nil {
		return "failed"
	}
	return "ok"
}
func (s *Server) checkHealth(network bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	s.health.RLock()
	v := s.health.value
	s.health.RUnlock()
	local, c := context.WithTimeout(ctx, 5*time.Second)
	out, err := exec.CommandContext(local, "/bin/sh", filepath.Join(s.options.Root, "health.sh")).Output()
	c()
	var interception map[string]any
	if json.Unmarshal(out, &interception) != nil {
		interception = map[string]any{"state": "unknown"}
	}
	if err != nil && interception["state"] == "checked" {
		interception["complete"] = false
	}
	v.Interception = interception
	v.CheckedAt = time.Now().Unix()
	v.UDP = "not-tested"
	if network {
		v.NetworkCheckedAt = time.Now().Unix()
		dnsCtx, dnsCancel := context.WithTimeout(ctx, 3*time.Second)
		resolver := net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", "127.0.0.1:1053")
		}}
		ips, e := resolver.LookupIPAddr(dnsCtx, "www.baidu.com")
		dnsCancel()
		v.DNS = "failed"
		if e == nil && len(ips) > 0 {
			v.DNS = "ok"
		}
		direct := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		v.DirectHTTP = probeHTTP(ctx, direct, "https://www.baidu.com/robots.txt", 200)
		proxyURL, _ := url.Parse("http://127.0.0.1:7890")
		user, password := "", ""
		cfg, _, e := s.store.Snapshot()
		if e == nil {
			if authentication, ok := cfg["authentication"].([]any); ok && len(authentication) > 0 {
				if credential, ok := authentication[0].(string); ok {
					var found bool
					user, password, found = strings.Cut(credential, ":")
					if found {
						proxyURL.User = url.UserPassword(user, password)
					}
				}
			}
		}
		proxy := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, CheckRedirect: direct.CheckRedirect}
		v.ProxyHTTP = probeHTTP(ctx, proxy, "https://www.google.com/generate_204", 204)
		v.ProxyUDP4, v.ProxyUDP6 = "not-tested", "not-tested"
		resolve, dnsCancel := context.WithTimeout(ctx, 2*time.Second)
		addresses, e := net.DefaultResolver.LookupIPAddr(resolve, "stun.l.google.com")
		dnsCancel()
		if e == nil {
			for _, family := range []int{4, 6} {
				for _, a := range addresses {
					if (a.IP.To4() != nil) == (family == 4) {
						result := probeUDP(ctx, "127.0.0.1:7890", user, password, a.IP, 19302)
						if family == 4 {
							v.ProxyUDP4 = result
						} else {
							v.ProxyUDP6 = result
						}
						break
					}
				}
			}
		}

	}
	s.health.Lock()
	s.health.value = v
	s.health.Unlock()
}

// No external test drives the fail-open watchdog. Public endpoints and node
// reachability are advisory; only local core/agent/rule failure triggers teardown.
func (s *Server) StartMaintenance(ctx context.Context) {
	go func() {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		round := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if round%10 == 0 {
					if service, ok := s.options.Service.(*ScriptService); ok {
						refresh, cancel := context.WithTimeout(ctx, 15*time.Second)
						_ = service.run(refresh, "relay-refresh")
						cancel()
					}
				}
				s.checkHealth(round%10 == 0)
				if round%10 == 0 {
					s.checkFailover()
				}
				round++
				timer.Reset(30 * time.Second)
			}
		}
	}()
}
func readPublicStage(run string) string {
	data, _ := os.ReadFile(filepath.Join(run, "bootstrap-stage"))
	if len(data) > 256 {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}
