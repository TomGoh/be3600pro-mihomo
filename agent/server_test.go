package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureToken = "a-long-management-token-for-tests-only"

type fixtureCore struct {
	root       string
	patches    int
	reloads    int
	selected   string
	failReload bool
}

func (c *fixtureCore) Request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if method == "PUT" && path == "/configs" {
		c.reloads++
		if c.failReload {
			c.failReload = false
			return nil, errors.New("simulated reload failure")
		}
		return json.RawMessage(`{}`), nil
	}
	if method == "PATCH" {
		c.patches++
		return json.RawMessage(`{}`), nil
	}
	if method == "PUT" && strings.HasPrefix(path, "/proxies/") {
		data, _ := json.Marshal(body)
		var v map[string]string
		_ = json.Unmarshal(data, &v)
		c.selected = v["name"]
		return json.RawMessage(`{}`), nil
	}
	if path == "/proxies" {
		return json.Marshal(map[string]any{"proxies": map[string]any{"Policy": map[string]any{"type": "Selector", "now": c.selected, "all": []string{"old-node", "new-node"}}, "__cdx_bootstrap": map[string]any{"type": "Fallback", "now": "old-node", "all": []string{"old-node"}}}})
	}
	if strings.HasPrefix(path, "/providers/proxies/") {
		data, _ := os.ReadFile(filepath.Join(c.root, "config.json"))
		var config map[string]any
		_ = json.Unmarshal(data, &config)
		providers, _ := config["proxy-providers"].(map[string]any)
		if providers[strings.TrimPrefix(path, "/providers/proxies/")] == nil {
			return nil, errors.New("provider not found")
		}
		return json.RawMessage(`{"proxies":[{"name":"sub-node"}]}`), nil
	}
	if path == "/providers/proxies" {
		return json.RawMessage(`{"providers":{}}`), nil
	}
	if path == "/version" {
		return json.RawMessage(`{"version":"fixture-core"}`), nil
	}
	if path == "/configs" {
		return json.RawMessage(`{"mode":"rule"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

type fixtureService struct {
	enabled bool
	starts  int
}

func (s *fixtureService) Enabled() bool                                 { return s.enabled }
func (s *fixtureService) SetEnabled(ctx context.Context, on bool) error { s.enabled = on; return nil }
func (s *fixtureService) StartCore(context.Context) error               { s.starts++; return nil }
func (s *fixtureService) StopCore(context.Context) error                { s.enabled = false; return nil }

func serverFixture(t *testing.T) (*Server, *fixtureCore, *fixtureService, string) {
	t.Helper()
	store, state := storeFixture(t)
	writeTestJSON(t, filepath.Join(store.Root, "config.json"), map[string]any{"mode": "rule", "proxies": []any{}, "proxy-groups": []any{map[string]any{"name": "Policy", "type": "select", "proxies": []string{"old-node", "new-node"}}}})
	writeTestJSON(t, filepath.Join(store.Root, "state.json"), state)
	core := &fixtureCore{root: store.Root, selected: "old-node"}
	service := &fixtureService{enabled: true}
	run := t.TempDir()
	server, err := NewServer(Options{Root: store.Root, RunDir: run, CorePath: filepath.Join(run, "mihomo"), Token: fixtureToken, Core: core, Service: service, Validate: func(context.Context, string) error { return nil }, Download: &http.Client{}, ArchiveSHA256: strings.Repeat("0", 64), ArchiveBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	return server, core, service, store.Root
}

func TestRejectedNewProviderRestoresWithoutDisablingWorkingProxy(t *testing.T) {
	s, core, service, root := serverFixture(t)
	core.failReload = true
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("proxies: [{name: sub-node, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: fixture}]"))
	}))
	defer remote.Close()
	body, _ := json.Marshal(map[string]string{"name": "Rejected source", "url": remote.URL})
	response := apiRequest(s, "POST", "/api/subscriptions", string(body), true)
	if response.Code != 500 {
		t.Fatalf("status=%d", response.Code)
	}
	if !service.enabled {
		t.Fatal("successful rollback unnecessarily disabled the working proxy")
	}
	if readTestMode(t, root) != "rule" {
		t.Fatal("rollback lost the previous configuration")
	}
}

func apiRequest(server *Server, method, path, body string, authorized bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authorized {
		req.Header.Set("Authorization", "Bearer "+fixtureToken)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	return response
}

func TestEveryManagementEndpointRequiresAuthentication(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	for _, path := range []string{"/api/status", "/api/groups", "/api/subscriptions", "/api/mode"} {
		r := apiRequest(s, "GET", path, "", false)
		if r.Code != 401 {
			t.Errorf("%s status=%d, want401", path, r.Code)
		}
	}
}

func TestInvalidModeDoesNotReachCoreOrChangeConfig(t *testing.T) {
	s, core, _, root := serverFixture(t)
	r := apiRequest(s, "PUT", "/api/mode", `{"mode":"invalid"}`, true)
	if r.Code != 400 {
		t.Fatalf("status=%d, want400", r.Code)
	}
	if core.patches != 0 || readTestMode(t, root) != "rule" {
		t.Fatal("invalid mode modified the running engine")
	}
}

func TestWrongCoreArchiveCannotReplaceOrStartCore(t *testing.T) {
	s, _, service, _ := serverFixture(t)
	if err := os.WriteFile(s.options.CorePath, []byte("previous core"), 0700); err != nil {
		t.Fatal(err)
	}
	r := apiRequest(s, "POST", "/api/core", "bad", true)
	if r.Code != 400 {
		t.Fatalf("status=%d, want400", r.Code)
	}
	data, _ := os.ReadFile(s.options.CorePath)
	if string(data) != "previous core" || service.starts != 0 {
		t.Fatal("invalid upload changed the core")
	}
}

func TestFailedSubscriptionDownloadKeepsCurrentConfigAndMasksURL(t *testing.T) {
	s, _, _, root := serverFixture(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "expired", 500) }))
	defer remote.Close()
	body, _ := json.Marshal(map[string]string{"name": "My source", "url": remote.URL + "/sub?token=private-query"})
	r := apiRequest(s, "POST", "/api/subscriptions", string(body), true)
	if r.Code != 502 {
		t.Fatalf("status=%d, want502", r.Code)
	}
	if strings.Contains(r.Body.String(), "private-query") {
		t.Fatal("download failure disclosed subscription token")
	}
	if readTestMode(t, root) != "rule" {
		t.Fatal("failed download changed configuration")
	}
	data, _ := os.ReadFile(filepath.Join(root, "state.json"))
	var state State
	_ = json.Unmarshal(data, &state)
	if len(state.Subscriptions) != 0 {
		t.Fatal("failed source was committed")
	}
}

func TestValidSubscriptionBecomesFileProviderAndListIsRedacted(t *testing.T) {
	s, _, _, root := serverFixture(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("external-controller: 0.0.0.0:1234\nproxies: [{name: sub-node, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: fixture}]"))
	}))
	defer remote.Close()
	body, _ := json.Marshal(map[string]string{"name": "My source", "url": remote.URL + "/sub?token=private-query"})
	r := apiRequest(s, "POST", "/api/subscriptions", string(body), true)
	if r.Code != 201 {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
	list := apiRequest(s, "GET", "/api/subscriptions", "", true)
	if strings.Contains(list.Body.String(), "private-query") {
		t.Fatal("list disclosed subscription credential")
	}
	configData, _ := os.ReadFile(filepath.Join(root, "config.json"))
	var cfg map[string]any
	_ = json.Unmarshal(configData, &cfg)
	providers := cfg["proxy-providers"].(map[string]any)
	if len(providers) != 1 {
		t.Fatal("provider missing")
	}
	for _, value := range providers {
		p := value.(map[string]any)
		if p["type"] != "file" || p["url"] != nil {
			t.Fatal("core can fetch unvalidated remote content")
		}
	}
	if cfg["external-controller"] != nil {
		t.Fatal("subscription overwrote trusted management configuration")
	}
}

func TestUnknownNodeIsRejectedWithoutChangingSelection(t *testing.T) {
	s, core, _, _ := serverFixture(t)
	r := apiRequest(s, "PUT", "/api/groups/Policy", `{"name":"not-in-list"}`, true)
	if r.Code != 400 || core.selected != "old-node" {
		t.Fatal("invalid node selection changed the core")
	}
}
