package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// The host-prepared minima distinguish an intentionally empty source from a
// provider that exists in the API but is still parsing its cached rules.
// Older installations remain compatible and are explicitly unverified.
func CheckRoutingReady(ctx context.Context, root string, core CoreAPI) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "routing-readiness.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	fail := errors.New("路由规则尚未就绪，请等待加载完成")
	if err != nil || len(raw) > 64*1024 {
		return true, fail
	}
	var manifest struct {
		Schema    int            `json:"schema"`
		Providers map[string]int `json:"providers"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != 1 || manifest.Providers == nil {
		return true, fail
	}
	cfgRaw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		return true, fail
	}
	var cfg struct {
		Providers map[string]json.RawMessage `json:"rule-providers"`
	}
	if json.Unmarshal(cfgRaw, &cfg) != nil {
		return true, fail
	}
	for name := range cfg.Providers {
		minimum, ok := manifest.Providers[name]
		if !ok || minimum < 0 || minimum > 1 {
			return true, fail
		}
	}
	if len(cfg.Providers) == 0 {
		return true, nil
	}
	body, err := core.Request(ctx, "GET", "/providers/rules", nil)
	if err != nil {
		return true, fail
	}
	var status struct {
		Providers map[string]struct {
			Count *int `json:"ruleCount"`
		} `json:"providers"`
	}
	if json.Unmarshal(body, &status) != nil {
		return true, fail
	}
	for name := range cfg.Providers {
		p, ok := status.Providers[name]
		if !ok || p.Count == nil || *p.Count < manifest.Providers[name] {
			return true, fail
		}
	}
	return true, nil
}
