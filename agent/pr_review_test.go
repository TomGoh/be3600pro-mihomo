package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rejectedManualSelectionCore struct{ CoreAPI }

func (c rejectedManualSelectionCore) Request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if method == "PUT" && path == "/proxies/Policy" {
		return nil, errors.New("temporary core failure")
	}
	return c.CoreAPI.Request(ctx, method, path, body)
}

func TestFailedManualSelectionPreservesAutomaticPolicy(t *testing.T) {
	for _, failure := range []string{"core", "state-space"} {
		t.Run(failure, func(t *testing.T) {
			s, core, _, root := serverFixture(t)
			_, state, _ := s.store.Snapshot()
			state.Selections["Policy"] = "old-node"
			writeTestJSON(t, filepath.Join(root, "state.json"), state)
			policyPath := filepath.Join(root, "failover.json")
			writeTestJSON(t, policyPath, []failoverPolicy{{Enabled: true, Group: "Policy", Candidates: []string{"old-node", "new-node"}}})
			before, err := os.ReadFile(policyPath)
			if err != nil {
				t.Fatal(err)
			}
			want := 502
			if failure == "core" {
				s.options.Core = rejectedManualSelectionCore{core}
			} else {
				want = 500
				s.store.FreeBytes = func(string) (uint64, error) { return 0, nil }
			}
			r := apiRequest(s, "PUT", "/api/groups/Policy", `{"name":"new-node"}`, true)
			if r.Code != want {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
			after, err := os.ReadFile(policyPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed manual switch disabled the existing automatic policy", err)
			}
			_, saved, err := s.store.Snapshot()
			if err != nil || saved.Selections["Policy"] != "old-node" || core.selected != "old-node" {
				t.Fatal("failed switch changed the saved or active node", err)
			}
		})
	}
}

func TestBackupRestoreValidatesExistingPersistentCache(t *testing.T) {
	for _, cache := range []string{"corrupt", "different", "checksum", "truncated", "matching"} {
		t.Run(cache, func(t *testing.T) {
			s, core, service, root := serverFixture(t)
			cfg, state, _ := s.store.Snapshot()
			path := filepath.Join(s.options.RunDir, "providers", "sub-0123456789ab-abc.json")
			payload := []byte(`{"proxies":[{"name":"saved-node"}]}`)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			state.Subscriptions = []Subscription{{ID: "sub-0123456789ab", Path: path, NodeCount: 1}}
			writeTestJSON(t, filepath.Join(root, "state.json"), state)
			if err := s.saveBackup(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			state.Subscriptions = nil
			cfg["mode"] = "global"
			writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
			writeTestJSON(t, filepath.Join(root, "state.json"), state)
			cachePath := filepath.Join(root, "subcache", filepath.Base(path)+".gz")
			if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
				t.Fatal(err)
			}
			persisted := []byte("invalid gzip")
			if cache != "corrupt" {
				var compressed bytes.Buffer
				z, err := gzip.NewWriterLevel(&compressed, gzip.NoCompression)
				if err != nil {
					t.Fatal(err)
				}
				z.Name = "different-encoding-is-valid"
				body := payload
				if cache == "different" {
					body = []byte(`{"proxies":[{"name":"wrong-node"}]}`)
				}
				if _, err = z.Write(body); err != nil {
					t.Fatal(err)
				}
				if err = z.Close(); err != nil {
					t.Fatal(err)
				}
				persisted = compressed.Bytes()
				if cache == "checksum" {
					persisted[len(persisted)-8] ^= 1
				} else if cache == "truncated" {
					persisted = persisted[:len(persisted)-3]
				}
			}
			if err := os.WriteFile(cachePath, persisted, 0600); err != nil {
				t.Fatal(err)
			}
			r := apiRequest(s, "POST", "/api/backup/restore", `{}`, true)
			if cache == "matching" {
				if r.Code != 200 || readTestMode(t, root) != "rule" {
					t.Fatalf("matching cache rejected: %d %s", r.Code, r.Body.String())
				}
				actual, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(actual, payload) {
					t.Fatal("restored runtime cache differs from backup", err)
				}
			} else {
				if r.Code != 400 || readTestMode(t, root) != "global" || core.reloads != 0 {
					t.Fatalf("bad persistent cache accepted: status=%d reloads=%d", r.Code, core.reloads)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("rejected restore left a new runtime cache")
				}
				_, saved, err := s.store.Snapshot()
				if err != nil || len(saved.Subscriptions) != 0 {
					t.Fatal("rejected restore changed subscriptions", err)
				}
			}
			actual, err := os.ReadFile(cachePath)
			if err != nil || !bytes.Equal(actual, persisted) {
				t.Fatal("restore overwrote existing persistent cache", err)
			}
			if service.Enabled() {
				t.Fatal("restore re-enabled interception")
			}
		})
	}
}

func TestManualSelectionPolicyFailureRestoresSavedAndActiveNode(t *testing.T) {
	s, core, _, root := serverFixture(t)
	_, state, _ := s.store.Snapshot()
	state.Selections["Policy"] = "old-node"
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	if err := os.WriteFile(filepath.Join(root, "failover.json"), []byte("invalid policy"), 0600); err != nil {
		t.Fatal(err)
	}
	r := apiRequest(s, "PUT", "/api/groups/Policy", `{"name":"new-node"}`, true)
	if r.Code != 500 {
		t.Fatal(r.Code, r.Body.String())
	}
	_, saved, err := s.store.Snapshot()
	if err != nil || saved.Selections["Policy"] != "old-node" || core.selected != "old-node" {
		t.Fatal("failed policy update did not restore saved and active selection", err)
	}
}

func TestImportedUpdateResponseMatchesCommittedMetadata(t *testing.T) {
	s, _, _, root := serverFixture(t)
	cfg, state, _ := s.store.Snapshot()
	cfg["proxies"] = []any{map[string]any{"name": "old-node", "type": "ss", "server": "old.invalid", "port": 443, "cipher": "aes-128-gcm", "password": "fixture"}}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`proxies: [{name: old-node, type: ss, server: old.invalid, port: 443, cipher: aes-128-gcm, password: fixture}, {name: new-node, type: ss, server: new.invalid, port: 443, cipher: aes-128-gcm, password: fixture}]`))
	}))
	defer remote.Close()
	state.Imported = &Subscription{ID: "imported", Name: "已导入配置的订阅", URL: remote.URL + "/?token=private-subscription-token", UpdatedAt: 1, NodeCount: 1}
	writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	started := time.Now().Unix()
	r := apiRequest(s, "POST", "/api/subscriptions/imported/update", `{}`, true)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var response struct {
		Subscription struct {
			NodeCount int   `json:"nodeCount"`
			UpdatedAt int64 `json:"updatedAt"`
		} `json:"subscription"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	_, saved, err := s.store.Snapshot()
	if err != nil || saved.Imported == nil {
		t.Fatal("updated subscription missing", err)
	}
	if response.Subscription.NodeCount != 2 || response.Subscription.UpdatedAt < started || response.Subscription.UpdatedAt != saved.Imported.UpdatedAt || response.Subscription.NodeCount != saved.Imported.NodeCount {
		t.Fatalf("response is stale: %s; stored=%+v", r.Body.String(), *saved.Imported)
	}
	if strings.Contains(r.Body.String(), "private-subscription-token") {
		t.Fatal("response leaked subscription credentials")
	}
}
