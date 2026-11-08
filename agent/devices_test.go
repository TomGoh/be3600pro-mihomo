package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gameFixtureMAC = "02:11:22:33:44:55"

func deviceFixture(t *testing.T) (*Server, *fixtureCore, *fixtureService, string) {
	s, c, v, root := serverFixture(t)
	s.options.LeasePaths = []string{}
	s.options.ARPPath = filepath.Join(root, "absent-arp")
	return s, c, v, root
}
func deviceCall(s *Server, method, path string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestDeviceBypassIsStagedWithoutTouchingRunningProxy(t *testing.T) {
	s, _, service, root := deviceFixture(t)
	before, _ := os.ReadFile(filepath.Join(root, "config.json"))
	w := deviceCall(s, "PUT", "/api/devices/"+gameFixtureMAC+"/bypass", `{"enabled":true}`)
	if w.Code != 200 {
		t.Fatalf("stage bypass HTTP%d", w.Code)
	}
	data, err := os.ReadFile(filepath.Join(root, "bypass-macs"))
	if err != nil || strings.TrimSpace(string(data)) != gameFixtureMAC {
		t.Fatal("MAC policy not persisted")
	}
	info, _ := os.Stat(filepath.Join(root, "bypass-macs"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("MAC policy is not private")
	}
	after, _ := os.ReadFile(filepath.Join(root, "config.json"))
	if !bytes.Equal(before, after) || !service.enabled || service.starts != 0 {
		t.Fatal("staging changed active core or configuration")
	}
	w = deviceCall(s, "GET", "/api/devices", "")
	var response struct {
		Devices []struct {
			MAC                         string `json:"mac"`
			Configured, Active, Pending bool
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Devices) != 1 {
		t.Fatal("saved device absent from inventory")
	}
	d := response.Devices[0]
	if d.MAC != gameFixtureMAC || !d.Configured || d.Active || !d.Pending {
		t.Fatal("configured/active distinction lost")
	}
}
func TestRemovingProtectionLeavesCurrentSnapshotUntilNextEnable(t *testing.T) {
	s, _, service, root := deviceFixture(t)
	_ = os.WriteFile(filepath.Join(root, "bypass-macs"), []byte(gameFixtureMAC+"\n"), 0600)
	_ = os.WriteFile(filepath.Join(s.options.RunDir, "bypass-active"), []byte(gameFixtureMAC+"\n"), 0600)
	w := deviceCall(s, "PUT", "/api/devices/"+gameFixtureMAC+"/bypass", `{"enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("remove HTTP%d", w.Code)
	}
	w = deviceCall(s, "GET", "/api/devices", "")
	var response struct {
		Devices []struct{ Configured, Active, Pending bool }
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if len(response.Devices) != 1 || response.Devices[0].Configured || !response.Devices[0].Active || !response.Devices[0].Pending || !service.enabled {
		t.Fatal("staged removal changed live protection or misreported active state")
	}
}
func TestInvalidBypassMACCannotChangeSavedPolicy(t *testing.T) {
	for _, mac := range []string{"not-a-mac", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "01:11:22:33:44:55", "02:11:22:33:44:55:66:77"} {
		t.Run(mac, func(t *testing.T) {
			s, _, _, root := deviceFixture(t)
			old := []byte(gameFixtureMAC + "\n")
			_ = os.WriteFile(filepath.Join(root, "bypass-macs"), old, 0600)
			w := deviceCall(s, "PUT", "/api/devices/"+mac+"/bypass", `{"enabled":true}`)
			if w.Code != 400 {
				t.Fatalf("invalid MAC HTTP%d", w.Code)
			}
			got, _ := os.ReadFile(filepath.Join(root, "bypass-macs"))
			if !bytes.Equal(got, old) {
				t.Fatal("invalid MAC changed policy")
			}
		})
	}
}

func TestDeviceInventoryUsesOnlyLANNeighboursAndKeepsSavedOfflineDevices(t *testing.T) {
	s, _, _, root := deviceFixture(t)
	lease := filepath.Join(root, "leases")
	arp := filepath.Join(root, "arp")
	_ = os.WriteFile(lease, []byte("0 "+gameFixtureMAC+" 192.168.31.125 Game-PC *\n0 02:aa:bb:cc:dd:ee 192.168.1.8 Other-Network *\n1 02:22:33:44:55:66 192.168.31.20 Expired *\n"), 0600)
	_ = os.WriteFile(arp, []byte("IP address HW type Flags HW address Mask Device\n192.168.31.125 0x1 0x2 "+gameFixtureMAC+" * br-lan\n192.168.31.9 0x1 0x2 02:77:66:55:44:22 * eth1\n"), 0600)
	s.options.LeasePaths = []string{lease}
	s.options.ARPPath = arp
	_ = os.WriteFile(filepath.Join(root, "bypass-macs"), []byte("02:33:44:55:66:77\n"), 0600)
	w := deviceCall(s, "GET", "/api/devices", "")
	var result struct{ Devices []lanDevice }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Devices) != 2 {
		t.Fatal("inventory included expired or non-LAN devices, or lost saved device")
	}
	found := false
	for _, d := range result.Devices {
		if d.MAC == gameFixtureMAC {
			found = d.IP == "192.168.31.125" && d.Name == "Game-PC" && d.Online
		}
	}
	if !found {
		t.Fatal("game PC identity was not joined from LAN lease and neighbour")
	}
}

func TestBypassPolicyRejectsThirtyThirdDeviceAndMissingAuth(t *testing.T) {
	s, _, _, root := deviceFixture(t)
	var text strings.Builder
	for i := 0; i < 32; i++ {
		text.WriteString(fmt.Sprintf("02:00:00:00:01:%02x\n", i))
	}
	_ = os.WriteFile(filepath.Join(root, "bypass-macs"), []byte(text.String()), 0600)
	w := deviceCall(s, "PUT", "/api/devices/"+gameFixtureMAC+"/bypass", `{"enabled":true}`)
	if w.Code != 400 {
		t.Fatal("33rd device accepted")
	}
	request := httptest.NewRequest("GET", "/api/devices", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatal("device inventory is not authenticated")
	}
}
