package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

type readinessCore struct {
	body  json.RawMessage
	calls int
}

func (c *readinessCore) Request(context.Context, string, string, any) (json.RawMessage, error) {
	c.calls++
	return c.body, nil
}
func TestRoutingReadinessRequiresEveryExpectedProviderAndAllowsKnownEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		minimum    int
		ready      bool
	}{{"loading", `{"providers":{"rules":{"ruleCount":0}}}`, 1, false}, {"ready", `{"providers":{"rules":{"ruleCount":97}}}`, 1, true}, {"missing", `{"providers":{}}`, 1, false}, {"known-empty", `{"providers":{"rules":{"ruleCount":0}}}`, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := storeFixture(t)
			writeTestJSON(t, filepath.Join(store.Root, "config.json"), map[string]any{"rule-providers": map[string]any{"rules": map[string]any{"type": "http"}}})
			writeTestJSON(t, filepath.Join(store.Root, "routing-readiness.json"), map[string]any{"schema": 1, "providers": map[string]int{"rules": tc.minimum}})
			core := &readinessCore{body: json.RawMessage(tc.body)}
			verified, err := CheckRoutingReady(context.Background(), store.Root, core)
			if !verified || (err == nil) != tc.ready {
				t.Fatalf("verified=%v err=%v", verified, err)
			}
		})
	}
}
func TestLegacyReadinessWithoutManifestIsExplicitlyUnverified(t *testing.T) {
	root := t.TempDir()
	core := &readinessCore{}
	verified, err := CheckRoutingReady(context.Background(), root, core)
	if verified || err != nil || core.calls != 0 {
		t.Fatal("legacy readiness falsely verified", verified, err)
	}
}
func TestReadinessRejectsUntrackedAddedProvider(t *testing.T) {
	store, _ := storeFixture(t)
	writeTestJSON(t, filepath.Join(store.Root, "config.json"), map[string]any{"rule-providers": map[string]any{"new": map[string]any{}}})
	writeTestJSON(t, filepath.Join(store.Root, "routing-readiness.json"), map[string]any{"schema": 1, "providers": map[string]int{}})
	_, err := CheckRoutingReady(context.Background(), store.Root, &readinessCore{})
	if err == nil {
		t.Fatal("new provider bypassed readiness gate")
	}
}
