package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBindingPreviewIsReadOnlyAndApplyPreservesMatchingSelection(t *testing.T) {
	s, core, _, root := serverFixture(t)
	cfg, state, _ := s.store.Snapshot()
	cfg["proxies"] = []any{map[string]any{"name": "old-node", "type": "ss", "server": "old.invalid", "port": 443, "cipher": "aes-128-gcm", "password": "old-secret"}}
	state.Selections["Policy"] = "old-node"
	writeTestJSON(t, filepath.Join(root, "config.json"), cfg)
	writeTestJSON(t, filepath.Join(root, "state.json"), state)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`proxies: [{name: old-node, type: ss, server: new.invalid, port: 443, cipher: aes-128-gcm, password: new-secret}]`))
	}))
	defer remote.Close()
	body, _ := json.Marshal(map[string]string{"url": remote.URL})
	r := apiRequest(s, "POST", "/api/imported/preview", string(body), true)
	if r.Code != 200 {
		t.Fatalf("preview=%d %s", r.Code, r.Body.String())
	}
	var preview struct {
		Token string `json:"token"`
	}
	json.Unmarshal(r.Body.Bytes(), &preview)
	if core.reloads != 0 {
		t.Fatal("preview reloaded core")
	}
	apply, _ := json.Marshal(map[string]any{"token": preview.Token, "acceptMissing": true})
	r = apiRequest(s, "POST", "/api/imported/apply", string(apply), true)
	if r.Code != 200 {
		t.Fatalf("apply=%d %s", r.Code, r.Body.String())
	}
	cfg, state, _ = s.store.Snapshot()
	if state.Imported == nil || state.Selections["Policy"] != "old-node" {
		t.Fatal("binding or matching choice lost")
	}
	if r = apiRequest(s, "POST", "/api/imported/apply", string(apply), true); r.Code != 409 {
		t.Fatal("preview token reused")
	}
}
