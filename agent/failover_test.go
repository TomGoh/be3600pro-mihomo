package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFailoverRequiresRepeatedFailureAndCooldown(t *testing.T) {
	state := failoverRuntime{}
	policy := failoverPolicy{Enabled: true, Group: "Policy", Candidates: []string{"a", "b"}}
	calls := 0
	probe := func(name string) bool { calls++; return name == "b" }
	now := time.Unix(1000, 0)
	if got := chooseFailover(policy, "a", &state, now, probe); got != "" {
		t.Fatal("switched on one transient failure")
	}
	if got := chooseFailover(policy, "a", &state, now.Add(5*time.Minute), probe); got != "b" {
		t.Fatalf("no fallback: %q", got)
	}
	before := calls
	if got := chooseFailover(policy, "a", &state, now.Add(6*time.Minute), probe); got != "" || calls != before {
		t.Fatal("ignored cooldown")
	}
}
func TestFailoverNeverOverridesManualChoiceOrDisabledPolicy(t *testing.T) {
	state := failoverRuntime{}
	p := failoverPolicy{Enabled: true, Candidates: []string{"a", "b"}}
	calls := 0
	probe := func(string) bool { calls++; return false }
	_ = chooseFailover(p, "manual", &state, time.Now(), probe)
	p.Enabled = false
	_ = chooseFailover(p, "a", &state, time.Now(), probe)
	if calls != 0 {
		t.Fatal("probed disabled/manual choice")
	}
}

func TestManualSelectionDisablesAutoPolicyAndPersistsChoice(t *testing.T) {
	s, core, _, root := serverFixture(t)
	writeTestJSON(t, filepath.Join(root, "failover.json"), []failoverPolicy{{Enabled: true, Group: "Policy", Candidates: []string{"old-node", "new-node"}}})
	r := apiRequest(s, "PUT", "/api/groups/Policy", `{"name":"new-node"}`, true)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	policies, err := s.failoverPolicies()
	if err != nil || len(policies) != 1 || policies[0].Enabled {
		t.Fatal("manual override did not disable automatic policy", err)
	}
	_, state, _ := s.store.Snapshot()
	if state.Selections["Policy"] != "new-node" || core.selected != "new-node" {
		t.Fatal("manual selection not preserved")
	}
}
