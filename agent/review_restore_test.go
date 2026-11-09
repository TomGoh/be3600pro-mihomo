package main

import (
	"path/filepath"
	"testing"
)

func TestReviewRestoreUsesBackupSelection(t *testing.T) {
	s, core, _, root := serverFixture(t)
	cfg, state, _ := s.store.Snapshot()
	state.Selections["Policy"] = "old-node"
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	if r := apiRequest(s, "POST", "/api/backup", `{}`, true); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	state.Selections["Policy"] = "new-node"
	core.selected = "new-node"
	writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	if r := apiRequest(s, "POST", "/api/backup/restore", `{}`, true); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	_, saved, _ := s.store.Snapshot()
	if saved.Selections["Policy"] != core.selected {
		t.Fatalf("restored state=%q but active core=%q", saved.Selections["Policy"], core.selected)
	}
}
func TestFailedRestoreReloadRestoresCurrentSelection(t *testing.T) {
	s, core, _, root := serverFixture(t)
	_, state, _ := s.store.Snapshot()
	state.Selections["Policy"] = "old-node"
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	if err := s.saveBackup(); err != nil {
		t.Fatal(err)
	}
	state.Selections["Policy"] = "new-node"
	core.selected = "new-node"
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	core.failReload = true
	r := apiRequest(s, "POST", "/api/backup/restore", `{}`, true)
	if r.Code != 500 {
		t.Fatalf("failed restore=%d", r.Code)
	}
	_, saved, _ := s.store.Snapshot()
	if core.selected != "new-node" || saved.Selections["Policy"] != "new-node" {
		t.Fatalf("rollback core=%s saved=%s", core.selected, saved.Selections["Policy"])
	}
}
