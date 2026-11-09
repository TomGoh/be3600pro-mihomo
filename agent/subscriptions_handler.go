package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var subscriptionID = regexp.MustCompile(`^sub-[0-9a-f]{12}$`)

func subscriptionGroup(sub Subscription) string { return "订阅 · " + sub.Name }
func publicSubscription(sub Subscription) map[string]any {
	return map[string]any{"id": sub.ID, "name": sub.Name, "url": MaskSubscriptionURL(sub.URL), "updatedAt": sub.UpdatedAt, "nodeCount": sub.NodeCount}
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	cfg, state, err := s.store.Snapshot()
	if err != nil {
		problem(w, 500, "无法读取订阅")
		return
	}
	items := []any{}
	if state.Imported != nil {
		items = append(items, publicSubscription(*state.Imported))
	}
	for _, sub := range state.Subscriptions {
		items = append(items, publicSubscription(sub))
	}
	// Imported inline nodes are real sources too, but no update URL can be
	// reconstructed from node credentials. Expose counts only, never credentials.
	inline, _ := cfg["proxies"].([]any)
	reply(w, 200, map[string]any{"subscriptions": items, "imported": map[string]any{"nodeCount": len(inline), "bound": state.Imported != nil}})
}

type subscriptionFailure struct {
	Status  int
	Message string
}

func (e *subscriptionFailure) Error() string         { return e.Message }
func sourceFailure(status int, message string) error { return &subscriptionFailure{status, message} }
func (s *Server) sourceProblem(w http.ResponseWriter, err error) {
	var failure *subscriptionFailure
	if errors.As(err, &failure) {
		s.recordError(failure.Message)
		problem(w, failure.Status, failure.Message)
		return
	}
	s.commitFailure(w, err)
}

func (s *Server) addSubscription(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if decodeBody(w, r, &input) != nil {
		problem(w, 400, "订阅参数格式不正确")
		return
	}
	sub, err := s.changeSubscription(r.Context(), "", strings.TrimSpace(input.Name), input.URL)
	if err != nil {
		s.sourceProblem(w, err)
		return
	}
	reply(w, 201, map[string]any{"subscription": publicSubscription(sub)})
}
func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == "imported" {
		s.updateImported(w, r)
		return
	}
	id := r.PathValue("id")
	if !subscriptionID.MatchString(id) {
		problem(w, 400, "订阅标识无效")
		return
	}
	sub, err := s.changeSubscription(r.Context(), id, "", "")
	if err != nil {
		s.sourceProblem(w, err)
		return
	}
	reply(w, 200, map[string]any{"subscription": publicSubscription(sub)})
}

func cloneState(state State) State {
	next := State{Imported: state.Imported, Subscriptions: append([]Subscription{}, state.Subscriptions...), Selections: map[string]string{}}
	for k, v := range state.Selections {
		next.Selections[k] = v
	}
	return next
}

func (s *Server) changeSubscription(ctx context.Context, id, name, sourceURL string) (Subscription, error) {
	var empty Subscription
	cfg, oldState, err := s.store.Snapshot()
	if err != nil {
		return empty, err
	}
	next := cloneState(oldState)
	index := -1
	for i, sub := range oldState.Subscriptions {
		if sub.ID == id {
			index = i
			name = sub.Name
			sourceURL = sub.URL
			break
		}
	}
	if id != "" && index < 0 {
		return empty, sourceFailure(404, "订阅不存在")
	}
	if id == "" {
		sources := len(oldState.Subscriptions)
		if oldState.Imported != nil {
			sources++
		}
		if sources >= 10 {
			return empty, sourceFailure(400, "当前路由器最多保存 10 份订阅")
		}
		if name == "" || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return empty, sourceFailure(400, "订阅名称须为 1 至 40 个可见字符")
		}
		for _, sub := range oldState.Subscriptions {
			if sub.Name == name {
				return empty, sourceFailure(400, "已有同名订阅")
			}
		}
		if groups, ok := cfg["proxy-groups"].([]any); ok {
			for _, raw := range groups {
				if group, ok := raw.(map[string]any); ok && group["name"] == "订阅 · "+name {
					return empty, sourceFailure(400, "此名称与已有策略组冲突")
				}
			}
		}
		var entropy [6]byte
		if _, err = rand.Read(entropy[:]); err != nil {
			return empty, err
		}
		id = "sub-" + hex.EncodeToString(entropy[:])
	}
	if err = ValidateSubscriptionURL(sourceURL); err != nil {
		return empty, sourceFailure(400, err.Error())
	}
	request, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
	if err != nil {
		return empty, sourceFailure(400, "订阅地址无效")
	}
	request.Header.Set("User-Agent", "RouterProxy/0.1 Mihomo")
	client := s.options.Download
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return empty, sourceFailure(502, "订阅下载失败，请检查地址和网络")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return empty, sourceFailure(502, fmt.Sprintf("订阅服务返回 HTTP %d，已保留原订阅", response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return empty, sourceFailure(502, "订阅内容未能完整下载")
	}
	normalized, count, err := NormalizeSubscription(body)
	if err != nil {
		return empty, sourceFailure(400, err.Error())
	}
	total := count
	if proxies, ok := cfg["proxies"].([]any); ok {
		total += len(proxies)
	}
	for i, sub := range oldState.Subscriptions {
		if i != index {
			total += sub.NodeCount
		}
	}
	if total > 2000 {
		return empty, sourceFailure(400, "节点总数超过此路由器的 2000 节点上限")
	}
	version := fmt.Sprintf("%s-%x.json", id, time.Now().UnixNano())
	runtimePath := filepath.Join(s.options.RunDir, "providers", version)
	if err = os.MkdirAll(filepath.Dir(runtimePath), 0700); err != nil {
		return empty, err
	}
	if err = os.WriteFile(runtimePath, normalized, 0600); err != nil {
		return empty, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(runtimePath)
		}
	}()
	var compressed bytes.Buffer
	zipper, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if _, err = zipper.Write(normalized); err != nil {
		return empty, err
	}
	if err = zipper.Close(); err != nil {
		return empty, err
	}
	persistentDir := filepath.Join(s.options.Root, "subcache")
	if err = os.MkdirAll(persistentDir, 0700); err != nil {
		return empty, err
	}
	persistentPath := filepath.Join(persistentDir, version+".gz")
	var existing uint64
	entries, _ := os.ReadDir(persistentDir)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".gz") {
			info, e := entry.Info()
			if e == nil {
				existing += uint64(info.Size())
			}
		}
	}
	if existing+uint64(compressed.Len()) > 512*1024 {
		return empty, sourceFailure(400, "订阅恢复缓存超过 512 KiB，请减少节点或订阅数量")
	}
	if err = s.store.requireSpace(uint64(compressed.Len()*2 + 128*1024)); err != nil {
		return empty, err
	}
	if err = atomicWrite(persistentPath, compressed.Bytes()); err != nil {
		return empty, err
	}
	defer func() {
		if !committed {
			_ = os.Remove(persistentPath)
		}
	}()
	sub := Subscription{ID: id, Name: name, URL: sourceURL, Path: runtimePath, UpdatedAt: time.Now().Unix(), NodeCount: count}
	if index < 0 {
		next.Subscriptions = append(next.Subscriptions, sub)
	} else {
		next.Subscriptions[index] = sub
	}
	var nodeList struct {
		Proxies []map[string]any `json:"proxies"`
	}
	_ = json.Unmarshal(normalized, &nodeList)
	names := map[string]bool{}
	for _, node := range nodeList.Proxies {
		if n, ok := node["name"].(string); ok {
			names[n] = true
		}
	}
	if selected, ok := next.Selections[subscriptionGroup(sub)]; ok && !names[selected] {
		delete(next.Selections, subscriptionGroup(sub))
	}
	if err = rebuildProviders(cfg, oldState, next); err != nil {
		return empty, err
	}
	reload := func(ctx context.Context) error {
		active, current, e := s.store.Snapshot()
		if e != nil {
			return e
		}
		if e = s.fullReload(ctx, current); e != nil {
			return e
		}
		providers, _ := active["proxy-providers"].(map[string]any)
		if providers[id] == nil {
			return nil
		}
		data, e := s.options.Core.Request(ctx, "GET", "/providers/proxies/"+id, nil)
		if e != nil {
			return e
		}
		var provider struct {
			Proxies []any `json:"proxies"`
		}
		if e = json.Unmarshal(data, &provider); e != nil {
			return e
		}
		if len(provider.Proxies) == 0 {
			return errors.New("节点订阅为空")
		}
		return nil
	}
	if err = s.commitConfig(ctx, cfg, next, s.options.Validate, reload); err != nil {
		return empty, err
	}
	committed = true
	if index >= 0 {
		s.removeSubscriptionCache(oldState.Subscriptions[index])
	}
	s.restoreSelections(ctx, next)
	s.recordError("")
	return sub, nil
}

func rebuildProviders(cfg map[string]any, old, next State) error {
	providers, ok := cfg["proxy-providers"].(map[string]any)
	if !ok {
		providers = map[string]any{}
	}
	oldGroups := map[string]bool{}
	for _, sub := range old.Subscriptions {
		delete(providers, sub.ID)
		oldGroups[subscriptionGroup(sub)] = true
	}
	groups, ok := cfg["proxy-groups"].([]any)
	if !ok {
		groups = []any{}
	}
	kept := []any{}
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			return errors.New("已有策略组格式不正确")
		}
		name, _ := group["name"].(string)
		if oldGroups[name] {
			continue
		}
		if group["type"] == "select" && !strings.HasPrefix(name, "__cdx_") {
			items, _ := group["proxies"].([]any)
			clean := []any{}
			for _, item := range items {
				n, _ := item.(string)
				if !oldGroups[n] {
					clean = append(clean, item)
				}
			}
			for _, sub := range next.Subscriptions {
				clean = append(clean, subscriptionGroup(sub))
			}
			group["proxies"] = clean
		}
		kept = append(kept, group)
	}
	for _, sub := range next.Subscriptions {
		providers[sub.ID] = map[string]any{"type": "file", "path": sub.Path}
		kept = append(kept, map[string]any{"name": subscriptionGroup(sub), "type": "select", "use": []string{sub.ID}})
	}
	cfg["proxy-providers"] = providers
	cfg["proxy-groups"] = kept
	return nil
}

func (s *Server) removeSubscriptionCache(sub Subscription) {
	if !subscriptionID.MatchString(sub.ID) {
		return
	}
	base := filepath.Base(sub.Path)
	if !strings.HasPrefix(base, sub.ID+"-") || !strings.HasSuffix(base, ".json") || filepath.Clean(filepath.Dir(sub.Path)) != filepath.Join(s.options.RunDir, "providers") {
		return
	}
	_ = os.Remove(sub.Path)
	_ = os.Remove(filepath.Join(s.options.Root, "subcache", base+".gz"))
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == "imported" {
		cfg, state, err := s.store.Snapshot()
		if err != nil || state.Imported == nil {
			problem(w, 404, "订阅不存在")
			return
		}
		state.Imported = nil
		err = s.commitConfig(r.Context(), cfg, state, s.options.Validate, func(context.Context) error { return nil })
		if err != nil {
			s.commitFailure(w, err)
			return
		}
		reply(w, 200, map[string]bool{"unbound": true})
		return
	}
	id := r.PathValue("id")
	if !subscriptionID.MatchString(id) {
		problem(w, 400, "订阅标识无效")
		return
	}
	cfg, old, err := s.store.Snapshot()
	if err != nil {
		problem(w, 500, "无法读取配置")
		return
	}
	next := cloneState(old)
	next.Subscriptions = []Subscription{}
	var removed *Subscription
	for _, sub := range old.Subscriptions {
		if sub.ID == id {
			copy := sub
			removed = &copy
		} else {
			next.Subscriptions = append(next.Subscriptions, sub)
		}
	}
	if removed == nil {
		problem(w, 404, "订阅不存在")
		return
	}
	for group, node := range next.Selections {
		if group == subscriptionGroup(*removed) || node == subscriptionGroup(*removed) {
			delete(next.Selections, group)
		}
	}
	if err = rebuildProviders(cfg, old, next); err != nil {
		problem(w, 500, "订阅配置无法更新")
		return
	}
	reload := func(ctx context.Context) error {
		_, current, e := s.store.Snapshot()
		if e != nil {
			return e
		}
		return s.fullReload(ctx, current)
	}
	if err = s.commitConfig(r.Context(), cfg, next, s.options.Validate, reload); err != nil {
		s.commitFailure(w, err)
		return
	}
	s.removeSubscriptionCache(*removed)
	s.restoreSelections(r.Context(), next)
	s.recordError("")
	reply(w, 200, map[string]bool{"deleted": true})
}
