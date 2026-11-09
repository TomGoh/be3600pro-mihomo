package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type failoverPolicy struct {
	Group      string   `json:"group"`
	Enabled    bool     `json:"enabled"`
	Candidates []string `json:"candidates"`
}
type failoverRuntime struct {
	Failures   int
	LastSwitch time.Time
}

func chooseFailover(p failoverPolicy, current string, state *failoverRuntime, now time.Time, probe func(string) bool) string {
	if !p.Enabled || !containsString(p.Candidates, current) || now.Sub(state.LastSwitch) < 10*time.Minute {
		return ""
	}
	if probe(current) {
		state.Failures = 0
		return ""
	}
	state.Failures++
	if state.Failures < 2 {
		return ""
	}
	for _, name := range p.Candidates {
		if name != current && probe(name) {
			state.LastSwitch = now
			state.Failures = 0
			return name
		}
	}
	return ""
}
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
func (s *Server) failoverPolicies() ([]failoverPolicy, error) {
	raw, err := os.ReadFile(filepath.Join(s.options.Root, "failover.json"))
	if os.IsNotExist(err) {
		return []failoverPolicy{}, nil
	}
	if err != nil || len(raw) > 8192 {
		return nil, errors.New("故障切换设置无法读取")
	}
	var policies []failoverPolicy
	err = json.Unmarshal(raw, &policies)
	if len(policies) > 2 {
		return nil, errors.New("故障切换设置超出限制")
	}
	return policies, err
}
func (s *Server) getFailover(w http.ResponseWriter, r *http.Request) {
	policies, err := s.failoverPolicies()
	if err != nil {
		problem(w, 500, "故障切换设置无法读取")
		return
	}
	reply(w, 200, map[string]any{"policies": policies, "intervalSeconds": 300, "failuresRequired": 2, "cooldownSeconds": 600})
}
func (s *Server) putFailover(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Policies []failoverPolicy `json:"policies"`
	}
	if decodeBody(w, r, &input) != nil || input.Policies == nil || len(input.Policies) > 2 {
		problem(w, 400, "最多启用 2 个策略组的故障切换")
		return
	}
	proxies, err := s.proxyMap(r.Context())
	if err != nil {
		problem(w, 503, "核心尚未就绪")
		return
	}
	seen := map[string]bool{}
	for _, p := range input.Policies {
		group, ok := proxies[p.Group]
		if !ok || group.Type != "Selector" || strings.HasPrefix(p.Group, "__cdx_") || seen[p.Group] || len(p.Candidates) < 2 || len(p.Candidates) > 4 {
			problem(w, 400, "请选择手动组及 2 至 4 个独立节点")
			return
		}
		seen[p.Group] = true
		nodes := map[string]bool{}
		for _, name := range p.Candidates {
			node, exists := proxies[name]
			if !exists || !containsString(group.All, name) || nodes[name] || len(node.All) > 0 || node.Type == "Selector" || node.Type == "URLTest" || node.Type == "Fallback" || node.Type == "LoadBalance" || node.Type == "Direct" || node.Type == "Reject" || node.Type == "DNS" || name == "DIRECT" || name == "REJECT" || strings.HasPrefix(name, "__cdx_") {
				problem(w, 400, "候选须为组内不重复的独立代理节点")
				return
			}
			nodes[name] = true
		}
	}
	raw, _ := json.Marshal(input.Policies)
	if err = s.store.requireSpace(uint64(len(raw)*2 + 4096)); err == nil {
		err = atomicWrite(filepath.Join(s.options.Root, "failover.json"), raw)
	}
	if err != nil {
		problem(w, 507, "故障切换设置未能保存")
		return
	}
	s.failoverState = map[string]*failoverRuntime{}
	reply(w, 200, map[string]any{"policies": input.Policies})
}
func (s *Server) disableManualFailover(group string) error {
	policies, err := s.failoverPolicies()
	if err != nil {
		return err
	}
	changed := false
	for i := range policies {
		if policies[i].Group == group && policies[i].Enabled {
			policies[i].Enabled = false
			changed = true
		}
	}
	if !changed {
		return nil
	}
	raw, _ := json.Marshal(policies)
	return atomicWrite(filepath.Join(s.options.Root, "failover.json"), raw)
}
func (s *Server) checkFailover() {
	if !s.options.Service.Enabled() || !s.mutation.TryLock() {
		return
	}
	defer s.mutation.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	s.intentMu.Lock()
	ctx = context.WithValue(ctx, operationKey{}, operationIntent{s.generation, readIntentEpoch(s.options.Root)})
	s.activeCancel = cancel
	s.intentMu.Unlock()
	defer func() { s.intentMu.Lock(); s.activeCancel = nil; s.intentMu.Unlock() }()
	policies, err := s.failoverPolicies()
	if err != nil || len(policies) == 0 {
		return
	}
	proxies, err := s.proxyMap(ctx)
	if err != nil {
		return
	}
	if s.failoverState == nil {
		s.failoverState = map[string]*failoverRuntime{}
	}
	for _, policy := range policies {
		p, ok := proxies[policy.Group]
		if !ok || p.Type != "Selector" {
			continue
		}
		if s.failoverState[policy.Group] == nil {
			s.failoverState[policy.Group] = &failoverRuntime{}
		}
		probe := func(node string) bool {
			if ctx.Err() != nil {
				return false
			}
			raw, e := s.options.Core.Request(ctx, "GET", "/proxies/"+url.PathEscape(node)+"/delay?timeout=3000&url="+url.QueryEscape("https://www.gstatic.com/generate_204"), nil)
			var value struct {
				Delay int `json:"delay"`
			}
			return e == nil && json.Unmarshal(raw, &value) == nil && value.Delay > 0
		}
		previous := *s.failoverState[policy.Group]
		selected := chooseFailover(policy, p.Now, s.failoverState[policy.Group], time.Now(), probe)
		if selected != "" && ctx.Err() == nil {
			if e := s.persistAutoSelection(ctx, policy.Group, p.Now, selected); e != nil {
				*s.failoverState[policy.Group] = previous
			}
		}
	}
}

func (s *Server) persistAutoSelection(ctx context.Context, group, old, node string) error {
	if _, err := s.options.Core.Request(ctx, "PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": node}); err != nil {
		return err
	}
	_, state, err := s.store.Snapshot()
	if err == nil {
		state.Selections[group] = node
		raw, _ := json.Marshal(state)
		err = s.store.requireSpace(uint64(len(raw) * 2))
		if err == nil {
			err = atomicWrite(filepath.Join(s.options.Root, "state.json"), raw)
		}
	}
	if err != nil {
		rollback, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.options.Core.Request(rollback, "PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": old})
	}
	return err
}
