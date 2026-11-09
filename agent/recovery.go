package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type recoveryBackup struct {
	Schema    int               `json:"schema"`
	Created   int64             `json:"createdAt"`
	Config    map[string]any    `json:"config"`
	State     State             `json:"state"`
	Providers map[string][]byte `json:"providers"`
}

func ownedProvider(sub Subscription, run string) bool {
	return subscriptionID.MatchString(sub.ID) && filepath.Clean(filepath.Dir(sub.Path)) == filepath.Join(run, "providers") && strings.HasPrefix(filepath.Base(sub.Path), sub.ID+"-") && strings.HasSuffix(sub.Path, ".json")
}
func (s *Server) saveBackup() error {
	cfg, state, err := s.store.Snapshot()
	if err != nil {
		return err
	}
	backup := recoveryBackup{Schema: 1, Created: time.Now().Unix(), Config: cfg, State: state, Providers: map[string][]byte{}}
	total := 0
	for _, sub := range state.Subscriptions {
		if !ownedProvider(sub, s.options.RunDir) {
			return errors.New("订阅缓存路径异常")
		}
		raw, e := os.ReadFile(sub.Path)
		if e != nil {
			return errors.New("订阅缓存尚未恢复，不能创建完整备份")
		}
		total += len(raw)
		if total > 4*1024*1024 {
			return errors.New("备份节点缓存过大")
		}
		backup.Providers[filepath.Base(sub.Path)] = raw
	}
	raw, err := json.Marshal(backup)
	if err != nil || len(raw) > 8*1024*1024 {
		return errors.New("备份超过大小限制")
	}
	var compressed bytes.Buffer
	zipper, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	_, err = zipper.Write(raw)
	if err == nil {
		err = zipper.Close()
	}
	if err != nil {
		return err
	}
	if compressed.Len() > 512*1024 {
		return errors.New("备份缓存超过 512 KiB")
	}
	if err = s.store.requireSpace(uint64(compressed.Len()*2 + 4096)); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.options.Root, "known-good.json.gz"), compressed.Bytes())
}
func (s *Server) loadBackup() (recoveryBackup, error) {
	var backup recoveryBackup
	raw, err := os.ReadFile(filepath.Join(s.options.Root, "known-good.json.gz"))
	if err != nil || len(raw) > 512*1024 {
		return backup, errors.New("没有有效备份")
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return backup, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, 8*1024*1024+1))
	if err != nil || len(body) > 8*1024*1024 {
		return backup, errors.New("备份校验失败")
	}
	if json.Unmarshal(body, &backup) != nil || backup.Schema != 1 || backup.Config == nil || backup.State.Selections == nil || len(backup.State.Subscriptions) > 10 {
		return backup, errors.New("备份格式无效")
	}
	for _, sub := range backup.State.Subscriptions {
		if !ownedProvider(sub, s.options.RunDir) || len(backup.Providers[filepath.Base(sub.Path)]) == 0 {
			return backup, errors.New("备份缺少节点缓存")
		}
	}
	return backup, nil
}
func (s *Server) backupStatus(w http.ResponseWriter, r *http.Request) {
	backup, err := s.loadBackup()
	reply(w, 200, map[string]any{"available": err == nil, "createdAt": backup.Created, "localOnly": true})
}
func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.options.Validate(ctx, filepath.Join(s.options.Root, "config.json")); err != nil {
		problem(w, 400, "当前配置尚未通过检查，未更新备份")
		return
	}
	if err := s.saveBackup(); err != nil {
		problem(w, 507, "无法保存完整备份，请检查闪存和节点缓存")
		return
	}
	reply(w, 200, map[string]bool{"saved": true})
}
func (s *Server) commitConfig(ctx context.Context, cfg map[string]any, state State, validate func(context.Context, string) error, reload func(context.Context) error) error {
	// Capture the previously loaded configuration before its provider cache can be removed.
	if err := s.saveBackup(); err != nil {
		return err
	}
	return s.store.Commit(ctx, cfg, state, validate, reload)
}
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	backup, err := s.loadBackup()
	_, beforeState, _ := s.store.Snapshot()
	if err != nil {
		problem(w, 400, "备份损坏或不存在，配置未改变")
		return
	}
	s.intentMu.Lock()
	s.generation++
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.intentMu.Unlock()
	// Teardown persists desired=off before any restored configuration is loaded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	if err = s.setEnabled(ctx, false); err != nil {
		problem(w, 503, "无法安全关闭接管，未恢复配置")
		return
	}
	created := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, p := range created {
				_ = os.Remove(p)
			}
		}
	}()
	for _, sub := range backup.State.Subscriptions {
		raw := backup.Providers[filepath.Base(sub.Path)]
		if len(raw) > 4*1024*1024 {
			problem(w, 400, "节点备份过大")
			return
		}
		// Existing versioned cache must agree; do not overwrite a live provider.
		if existing, e := os.ReadFile(sub.Path); e == nil {
			if !bytes.Equal(existing, raw) {
				problem(w, 400, "节点缓存冲突")
				return
			}
		} else {
			if err = os.MkdirAll(filepath.Dir(sub.Path), 0700); err != nil {
				problem(w, 500, "无法恢复节点缓存")
				return
			}
			if err = atomicWrite(sub.Path, raw); err != nil {
				problem(w, 500, "无法恢复节点缓存")
				return
			}
			created = append(created, sub.Path)
		}
		var compressed bytes.Buffer
		z := gzip.NewWriter(&compressed)
		_, _ = z.Write(raw)
		_ = z.Close()
		cache := filepath.Join(s.options.Root, "subcache", filepath.Base(sub.Path)+".gz")
		if _, e := os.Stat(cache); os.IsNotExist(e) {
			if err = s.store.requireSpace(uint64(compressed.Len()*2 + 4096)); err != nil {
				problem(w, 507, "节点恢复缓存空间不足")
				return
			}
			_ = os.MkdirAll(filepath.Dir(cache), 0700)
			if err = atomicWrite(cache, compressed.Bytes()); err != nil {
				problem(w, 500, "无法恢复节点持久缓存")
				return
			}
			created = append(created, cache)
		}
	}
	err = s.store.Commit(ctx, backup.Config, backup.State, s.options.Validate, func(ctx context.Context) error {
		_, current, e := s.store.Snapshot()
		if e != nil {
			return e
		}
		return s.fullReload(ctx, current)
	})
	if err != nil {
		s.commitFailure(w, err)
		return
	}
	committed = true
	keep := map[string]bool{}
	for _, sub := range backup.State.Subscriptions {
		keep[sub.Path] = true
	}
	for _, sub := range beforeState.Subscriptions {
		if !keep[sub.Path] {
			s.removeSubscriptionCache(sub)
		}
	}
	reply(w, 200, map[string]bool{"restored": true, "enabled": false})
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	// Positive allowlist: never include config, node names, URLs, IPs, MACs,
	// request bodies, arbitrary error text, logs, tokens or TLS material.
	cfg, state, err := s.store.Snapshot()
	if err != nil {
		problem(w, 500, "诊断信息暂不可用")
		return
	}
	mode, _ := cfg["mode"].(string)
	if mode != "rule" && mode != "global" && mode != "direct" {
		mode = "unknown"
	}
	free, _ := availableBytes(s.options.Root)
	reply(w, 200, map[string]any{"schema": 1, "createdAt": time.Now().Unix(), "enabled": s.options.Service.Enabled(), "busy": s.busy.Load(), "mode": mode, "managedSubscriptions": len(state.Subscriptions), "importedBound": state.Imported != nil, "flashFreeBytes": free, "health": s.diagnosticHealth(), "privacy": "allowlisted; configuration and logs excluded"})
}

func (s *Server) diagnosticHealth() map[string]any {
	s.health.RLock()
	v := s.health.value
	s.health.RUnlock()
	allowed := func(text string) string {
		switch text {
		case "ok", "failed", "not-tested":
			return text
		default:
			return "unknown"
		}
	}
	interception := map[string]any{}
	for _, key := range []string{"expectedEnabled", "ipv4", "ipv6", "inactive", "complete"} {
		if value, ok := v.Interception[key].(bool); ok {
			interception[key] = value
		}
	}
	return map[string]any{"checkedAt": v.CheckedAt, "networkCheckedAt": v.NetworkCheckedAt, "interception": interception, "dns": allowed(v.DNS), "directHttp": allowed(v.DirectHTTP), "proxyHttp": allowed(v.ProxyHTTP), "udp": allowed(v.UDP), "proxyUdp4": allowed(v.ProxyUDP4), "proxyUdp6": allowed(v.ProxyUDP6)}
}
