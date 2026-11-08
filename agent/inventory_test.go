package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportedConfigurationIsVisibleWithoutInventingASubscriptionURL(t *testing.T) {
	s, _, _, root := serverFixture(t)
	writeTestJSON(t, filepath.Join(root, "config.json"), map[string]any{"proxies": []any{map[string]any{"name": "one", "password": "private-fixture"}, map[string]any{"name": "two"}}})
	w := deviceCall(s, "GET", "/api/subscriptions", "")
	var r struct {
		Imported struct {
			NodeCount int `json:"nodeCount"`
		} `json:"imported"`
		Subscriptions []any `json:"subscriptions"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.Imported.NodeCount != 2 || len(r.Subscriptions) != 0 {
		t.Fatalf("imported source missing: HTTP%d", w.Code)
	}
	if string(w.Body.Bytes()) == "" || strings.Contains(w.Body.String(), "private-fixture") {
		t.Fatal("node credential disclosed")
	}
}

func TestBatchBypassReplacesWholeListWithoutRestartingCore(t *testing.T) {
	s, _, service, root := deviceFixture(t)
	_ = os.WriteFile(filepath.Join(root, "bypass-macs"), []byte(gameFixtureMAC+"\n"), 0600)
	w := deviceCall(s, "PUT", "/api/devices/bypass", `{"macs":["02:aa:bb:cc:dd:01","02:aa:bb:cc:dd:02"]}`)
	if w.Code != 200 {
		t.Fatalf("batch HTTP%d", w.Code)
	}
	got, _ := readBypassPolicy(filepath.Join(root, "bypass-macs"))
	if len(got) != 2 || got[gameFixtureMAC] || !got["02:aa:bb:cc:dd:01"] || !service.enabled || service.starts != 0 {
		t.Fatal("batch not atomic or disturbed core")
	}
}

func TestInvalidBatchCannotPartiallySaveAndEmptyBatchClears(t *testing.T) {
	s, _, _, root := deviceFixture(t)
	old := []byte(gameFixtureMAC + "\n")
	_ = os.WriteFile(filepath.Join(root, "bypass-macs"), old, 0600)
	for _, body := range []string{`{"macs":["02:aa:bb:cc:dd:01","invalid"]}`, `{}`, `{"macs":null}`} {
		w := deviceCall(s, "PUT", "/api/devices/bypass", body)
		got, _ := os.ReadFile(filepath.Join(root, "bypass-macs"))
		if w.Code != 400 || string(got) != string(old) {
			t.Fatal("invalid batch changed stored list")
		}
	}
	w := deviceCall(s, "PUT", "/api/devices/bypass", `{"macs":[]}`)
	got, _ := readBypassPolicy(filepath.Join(root, "bypass-macs"))
	if w.Code != 200 || len(got) != 0 {
		t.Fatal("empty selection did not clear list")
	}
}
