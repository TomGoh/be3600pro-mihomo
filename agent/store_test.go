package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestMode(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config["mode"].(string)
}

func storeFixture(t *testing.T) (*Store, State) {
	t.Helper()
	root := t.TempDir()
	state := State{Subscriptions: []Subscription{}, Selections: map[string]string{"Policy": "old-node"}}
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"mode": "rule", "proxies": []any{}})
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	return &Store{Root: root}, state
}

func TestCommitPersistsConfigStateAndPrivatePermissions(t *testing.T) {
	store, state := storeFixture(t)
	state.Selections["Policy"] = "new-node"
	err := store.Commit(context.Background(), map[string]any{"mode": "global"}, state,
		func(ctx context.Context, path string) error {
			if readTestMode(t, store.Root) != "rule" {
				t.Fatal("active file changed before validation")
			}
			if path == filepath.Join(store.Root, "config.json") {
				t.Fatal("validator received active file")
			}
			return nil
		}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestMode(t, store.Root); got != "global" {
		t.Fatalf("persisted mode=%q, want global", got)
	}
	data, err := os.ReadFile(filepath.Join(store.Root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved State
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Selections["Policy"] != "new-node" {
		t.Fatal("selection state was not committed")
	}
	for _, name := range []string{"config.json", "state.json"} {
		info, err := os.Stat(filepath.Join(store.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions=%o", name, info.Mode().Perm())
		}
	}
}

func TestValidationFailureKeepsActiveFilesAndDoesNotReload(t *testing.T) {
	store, state := storeFixture(t)
	reloads := 0
	err := store.Commit(context.Background(), map[string]any{"mode": "global"}, state,
		func(context.Context, string) error { return errors.New("invalid provider") },
		func(context.Context) error { reloads++; return nil })
	if err == nil {
		t.Fatal("invalid configuration was accepted")
	}
	if readTestMode(t, store.Root) != "rule" || reloads != 0 {
		t.Fatal("invalid configuration changed the active engine")
	}
}

func TestFailedReloadRestoresFileAndRunningEngine(t *testing.T) {
	store, state := storeFixture(t)
	engineMode := "rule"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := readTestMode(t, store.Root)
		if mode == "global" {
			engineMode = mode
			http.Error(w, "simulated reload failure", 500)
			return
		}
		engineMode = mode
		w.WriteHeader(204)
	}))
	defer server.Close()
	reload := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, "PUT", server.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 204 {
			return errors.New("core rejected reload")
		}
		return nil
	}
	err := store.Commit(context.Background(), map[string]any{"mode": "global"}, state,
		func(context.Context, string) error { return nil }, reload)
	if err == nil {
		t.Fatal("failed engine reload was reported as success")
	}
	if readTestMode(t, store.Root) != "rule" || engineMode != "rule" {
		t.Fatal("previous working engine/configuration was lost")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "transaction.json")); !os.IsNotExist(err) {
		t.Fatal("completed rollback left a pending transaction")
	}
}

func TestDiskReserveFailureKeepsConfiguration(t *testing.T) {
	store, state := storeFixture(t)
	store.FreeBytes = func(string) (uint64, error) { return 0, nil }
	err := store.Commit(context.Background(), map[string]any{"mode": "global"}, state,
		func(context.Context, string) error { return nil }, func(context.Context) error { return nil })
	if err == nil {
		t.Fatal("configuration commit ignored the flash reserve")
	}
	if readTestMode(t, store.Root) != "rule" {
		t.Fatal("low-space failure changed the active configuration")
	}
}

func TestInterruptedTransactionRestoresLastCommittedState(t *testing.T) {
	store, oldState := storeFixture(t)
	oldConfig, _ := os.ReadFile(filepath.Join(store.Root, "config.json"))
	oldStateJSON, _ := os.ReadFile(filepath.Join(store.Root, "state.json"))
	writeTestJSON(t, filepath.Join(store.Root, "transaction.json"), map[string]any{"config": json.RawMessage(oldConfig), "state": json.RawMessage(oldStateJSON)})
	writeTestJSON(t, filepath.Join(store.Root, "config.json"), map[string]any{"mode": "global"})
	writeTestJSON(t, filepath.Join(store.Root, "state.json"), State{Selections: map[string]string{"Policy": "uncommitted"}})
	if err := RecoverStore(store.Root); err != nil {
		t.Fatal(err)
	}
	if readTestMode(t, store.Root) != "rule" {
		t.Fatal("interrupted transaction was not recovered")
	}
	data, _ := os.ReadFile(filepath.Join(store.Root, "state.json"))
	var saved State
	_ = json.Unmarshal(data, &saved)
	if saved.Selections["Policy"] != oldState.Selections["Policy"] {
		t.Fatal("uncommitted selection survived recovery")
	}
}
