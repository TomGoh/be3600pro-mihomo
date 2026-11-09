package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticExportDoesNotLeakSecretsOrDeviceIdentity(t *testing.T) {
	s, _, _, root := serverFixture(t)
	cfg, state, _ := s.store.Snapshot()
	s.health.value.Interception = map[string]any{"unexpected": "canary-health-secret"}
	s.health.value.DNS = "canary-dns-secret"
	cfg["secret"] = "canary-controller-secret"
	cfg["authentication"] = []any{"canary-user:canary-pass"}
	cfg["proxies"] = []any{map[string]any{"name": "canary-node", "server": "203.0.113.77", "password": "canary-proxy-secret"}}
	state.Imported = &Subscription{URL: "https://canary.invalid/private?token=canary-subscription-token"}
	writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	r := apiRequest(s, "GET", "/api/diagnostics", "", true)
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	for _, text := range []string{"canary", "203.0.113.77", fixtureToken} {
		if strings.Contains(r.Body.String(), text) {
			t.Fatalf("diagnostic leaked %q", text)
		}
	}
}
func TestKnownGoodRestoreLeavesProxyOffAndCorruptionCannotWrite(t *testing.T) {
	s, _, service, root := serverFixture(t)
	if r := apiRequest(s, "POST", "/api/backup", `{}`, true); r.Code != 200 {
		t.Fatalf("backup=%d %s", r.Code, r.Body.String())
	}
	cfg, state, _ := s.store.Snapshot()
	cfg["mode"] = "global"
	writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	if r := apiRequest(s, "POST", "/api/backup/restore", `{}`, true); r.Code != 200 {
		t.Fatalf("restore=%d %s", r.Code, r.Body.String())
	}
	if service.Enabled() || readTestMode(t, root) != "rule" {
		t.Fatal("restore changed intent or wrong config")
	}
	os.WriteFile(filepath.Join(root, "known-good.json.gz"), []byte("corrupt"), 0600)
	if r := apiRequest(s, "POST", "/api/backup/restore", `{}`, true); r.Code != 400 || readTestMode(t, root) != "rule" {
		t.Fatal("corrupt backup modified config")
	}
}

func TestBackupRestoresManagedProviderAfterOldCacheRemoved(t *testing.T) {
	s, _, _, root := serverFixture(t)
	cfg, state, _ := s.store.Snapshot()
	path := filepath.Join(s.options.RunDir, "providers", "sub-0123456789ab-abc.json")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte(`{"proxies":[]}`), 0600)
	state.Subscriptions = []Subscription{{ID: "sub-0123456789ab", Path: path, NodeCount: 1}}
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	if err := s.saveBackup(); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	state.Subscriptions = nil
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	cfg["mode"] = "global"
	writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
	r := apiRequest(s, "POST", "/api/backup/restore", `{}`, true)
	if r.Code != 200 {
		t.Fatalf("restore=%d %s", r.Code, r.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("runtime cache missing", err)
	}
	if _, err := os.Stat(filepath.Join(root, "subcache", filepath.Base(path)+".gz")); err != nil {
		t.Fatal("persistent cache missing", err)
	}
}
