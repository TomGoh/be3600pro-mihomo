package main

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type lanDevice struct {
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	Name       string `json:"name"`
	Online     bool   `json:"online"`
	Configured bool   `json:"configured"`
	Active     bool   `json:"active"`
	Pending    bool   `json:"pending"`
}

func canonicalDeviceMAC(raw string) (string, error) {
	mac, err := net.ParseMAC(raw)
	if err != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" {
		return "", errors.New("请使用有效的单播设备 MAC 地址")
	}
	return mac.String(), nil
}
func readBypassPolicy(path string) (map[string]bool, error) {
	result := map[string]bool{}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 128), 1024)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		mac, e := canonicalDeviceMAC(raw)
		if e != nil {
			return nil, e
		}
		result[mac] = true
		if len(result) > 32 {
			return nil, errors.New("最多保存 32 台直连设备")
		}
	}
	return result, scanner.Err()
}
func (s *Server) bypassPolicies() (map[string]bool, map[string]bool, error) {
	configured, err := readBypassPolicy(filepath.Join(s.options.Root, "bypass-macs"))
	if err != nil {
		return nil, nil, err
	}
	active := map[string]bool{}
	if s.options.Service.Enabled() {
		active, err = readBypassPolicy(filepath.Join(s.options.RunDir, "bypass-active"))
	}
	return configured, active, err
}
func lanIP(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	return err == nil && netip.MustParsePrefix("192.168.31.0/24").Contains(ip) && raw != "192.168.31.1" && raw != "192.168.31.0" && raw != "192.168.31.255"
}
func scanDeviceFile(path string, visit func([]string)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 512), 4096)
	for i := 0; i < 2048 && scanner.Scan(); i++ {
		visit(strings.Fields(scanner.Text()))
	}
}
func discoverDevices(leases []string, arp string) map[string]*lanDevice {
	devices := map[string]*lanDevice{}
	now := time.Now().Unix()
	for _, path := range leases {
		scanDeviceFile(path, func(f []string) {
			if len(f) < 4 || !lanIP(f[2]) {
				return
			}
			expires, e := strconv.ParseInt(f[0], 10, 64)
			if e != nil || (expires != 0 && expires < now) {
				return
			}
			mac, e := canonicalDeviceMAC(f[1])
			if e != nil {
				return
			}
			name := f[3]
			if name == "*" {
				name = "局域网设备"
			}
			if len(name) > 128 {
				name = name[:128]
			}
			devices[mac] = &lanDevice{MAC: mac, IP: f[2], Name: name}
		})
	}
	scanDeviceFile(arp, func(f []string) {
		if len(f) < 6 || f[5] != "br-lan" || f[2] != "0x2" || !lanIP(f[0]) {
			return
		}
		mac, e := canonicalDeviceMAC(f[3])
		if e != nil {
			return
		}
		d := devices[mac]
		if d == nil {
			d = &lanDevice{MAC: mac, Name: "局域网设备"}
			devices[mac] = d
		}
		d.IP = f[0]
		d.Online = true
	})
	return devices
}
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	configured, active, err := s.bypassPolicies()
	if err != nil {
		problem(w, 500, "设备直连设置读取失败，请检查保存的 MAC 列表")
		return
	}
	leases := s.options.LeasePaths
	if leases == nil {
		leases = []string{"/tmp/dhcp.leases"}
	}
	arp := s.options.ARPPath
	if arp == "" {
		arp = "/proc/net/arp"
	}
	devices := discoverDevices(leases, arp)
	for _, policy := range []map[string]bool{configured, active} {
		for mac := range policy {
			if devices[mac] == nil {
				devices[mac] = &lanDevice{MAC: mac, Name: "已保存设备"}
			}
		}
	}
	names := make([]string, 0, len(devices))
	for mac := range devices {
		names = append(names, mac)
	}
	sort.Strings(names)
	result := make([]lanDevice, 0, len(names))
	for _, mac := range names {
		d := devices[mac]
		d.Configured = configured[mac]
		d.Active = active[mac]
		d.Pending = d.Configured != d.Active
		result = append(result, *d)
	}
	reply(w, 200, map[string]any{"devices": result, "proxyEnabled": s.options.Service.Enabled(), "applyOnNextEnable": true})
}
func (s *Server) setDeviceBypass(w http.ResponseWriter, r *http.Request) {
	mac, err := canonicalDeviceMAC(r.PathValue("mac"))
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if decodeBody(w, r, &input) != nil || input.Enabled == nil {
		problem(w, 400, "请提供设备直连开关")
		return
	}
	configured, active, err := s.bypassPolicies()
	if err != nil {
		problem(w, 500, "已有设备直连设置无法读取")
		return
	}
	if *input.Enabled {
		configured[mac] = true
	} else {
		delete(configured, mac)
	}
	if len(configured) > 32 {
		problem(w, 400, "最多保存 32 台直连设备")
		return
	}
	names := make([]string, 0, len(configured))
	for n := range configured {
		names = append(names, n)
	}
	sort.Strings(names)
	data := []byte(strings.Join(names, "\n"))
	if len(data) > 0 {
		data = append(data, '\n')
	}
	if err = s.store.requireSpace(uint64(len(data)*2 + 4096)); err != nil {
		problem(w, 507, "闪存余量不足，设备设置未改变")
		return
	}
	if err = atomicWrite(filepath.Join(s.options.Root, "bypass-macs"), data); err != nil {
		problem(w, 500, "设备直连设置保存失败")
		return
	}
	reply(w, 200, map[string]any{"mac": mac, "configured": configured[mac], "active": active[mac], "pending": configured[mac] != active[mac], "applyOnNextEnable": true})
}

// The checkbox list is a single replacement transaction. Invalid entries must
// never leave the user with only part of their selection saved.
func (s *Server) setDeviceBypassList(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MACs *[]string `json:"macs"`
	}
	if decodeBody(w, r, &input) != nil || input.MACs == nil || len(*input.MACs) > 32 {
		problem(w, 400, "请提供最多 32 台设备的直连名单")
		return
	}
	set := map[string]bool{}
	for _, raw := range *input.MACs {
		mac, err := canonicalDeviceMAC(raw)
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		set[mac] = true
	}
	names := make([]string, 0, len(set))
	for mac := range set {
		names = append(names, mac)
	}
	sort.Strings(names)
	data := []byte(strings.Join(names, "\n"))
	if len(data) > 0 {
		data = append(data, '\n')
	}
	if err := s.store.requireSpace(uint64(len(data)*2 + 4096)); err != nil {
		problem(w, 507, "闪存余量不足，设备设置未改变")
		return
	}
	if err := atomicWrite(filepath.Join(s.options.Root, "bypass-macs"), data); err != nil {
		problem(w, 500, "设备直连设置保存失败")
		return
	}
	reply(w, 200, map[string]any{"saved": len(set), "applyOnNextEnable": true})
}
