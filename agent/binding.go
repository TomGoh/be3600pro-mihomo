package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

type importedPreview struct {
	URL        string
	Nodes      []any
	At         time.Time
	Digest     [32]byte
	Missing    []string
	generation uint64
}
type previewStore struct {
	sync.Mutex
	items map[string]importedPreview
}

func configDigest(cfg map[string]any, state State) [32]byte {
	raw, _ := json.Marshal([]any{cfg, state})
	return sha256.Sum256(raw)
}
func (s *Server) fetchSource(ctx context.Context, source string) ([]any, error) {
	if err := ValidateSubscriptionURL(source); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return nil, errors.New("订阅地址无效")
	}
	req.Header.Set("User-Agent", "RouterProxy/0.3 Mihomo")
	client := s.options.Download
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("订阅下载失败")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("订阅服务未返回成功内容")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return nil, errors.New("订阅下载不完整")
	}
	normalized, _, err := NormalizeSubscription(raw)
	if err != nil {
		return nil, err
	}
	var result struct {
		Proxies []any `json:"proxies"`
	}
	err = json.Unmarshal(normalized, &result)
	return result.Proxies, err
}
func nodeNames(nodes []any) map[string]bool {
	names := map[string]bool{}
	for _, raw := range nodes {
		if n, ok := raw.(map[string]any); ok {
			if name, ok := n["name"].(string); ok {
				names[name] = true
			}
		}
	}
	return names
}
func missingNames(old, next []any) []string {
	names := nodeNames(next)
	missing := []string{}
	for name := range nodeNames(old) {
		if !names[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
func (s *Server) previewImported(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if decodeBody(w, r, &input) != nil {
		problem(w, 400, "订阅地址格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	nodes, err := s.fetchSource(ctx, input.URL)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	cfg, state, err := s.store.Snapshot()
	if err != nil {
		problem(w, 500, "配置无法读取")
		return
	}
	inline, _ := cfg["proxies"].([]any)
	missing := missingNames(inline, nodes)
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		problem(w, 500, "无法创建预览")
		return
	}
	token := hex.EncodeToString(entropy[:])
	s.previews.Lock()
	defer s.previews.Unlock()
	if s.previews.items == nil {
		s.previews.items = map[string]importedPreview{}
	}
	for id, v := range s.previews.items {
		if time.Since(v.At) > 10*time.Minute {
			delete(s.previews.items, id)
		}
	}
	if len(s.previews.items) >= 2 {
		problem(w, 429, "预览已满，请稍后重试")
		return
	}
	s.intentMu.Lock()
	generation := s.generation
	s.intentMu.Unlock()
	s.previews.items[token] = importedPreview{input.URL, nodes, time.Now(), configDigest(cfg, state), missing, generation}
	reply(w, 200, map[string]any{"token": token, "oldCount": len(inline), "newCount": len(nodes), "missing": missing, "expiresInSeconds": 600, "requiresConfirmation": len(missing) > 0})
}
func applyImportedNodes(cfg map[string]any, state *State, nodes []any) {
	old, _ := cfg["proxies"].([]any)
	oldNames, nextNames := nodeNames(old), nodeNames(nodes)
	groups, _ := cfg["proxy-groups"].([]any)
	for _, raw := range groups {
		g, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		list, ok := g["proxies"].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		used := map[string]bool{}
		hasInline := false
		for _, v := range list {
			name, _ := v.(string)
			if oldNames[name] {
				hasInline = true
				if !nextNames[name] {
					continue
				}
			}
			kept = append(kept, v)
			used[name] = true
		}
		if hasInline {
			for _, v := range nodes {
				n, _ := v.(map[string]any)
				name, _ := n["name"].(string)
				if !used[name] {
					kept = append(kept, name)
				}
			}
		}
		if len(kept) == 0 {
			kept = []any{"DIRECT"}
		}
		g["proxies"] = kept
	}
	for group, node := range state.Selections {
		if oldNames[node] && !nextNames[node] {
			delete(state.Selections, group)
		}
	}
	cfg["proxies"] = nodes
}
func (s *Server) applyImported(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token         string `json:"token"`
		AcceptMissing bool   `json:"acceptMissing"`
	}
	if decodeBody(w, r, &input) != nil {
		problem(w, 400, "预览参数无效")
		return
	}
	s.previews.Lock()
	preview, ok := s.previews.items[input.Token]
	delete(s.previews.items, input.Token)
	s.previews.Unlock()
	s.intentMu.Lock()
	generation := s.generation
	s.intentMu.Unlock()
	if !ok || time.Since(preview.At) > 10*time.Minute || preview.generation != generation {
		problem(w, 409, "预览已失效，请重新预览")
		return
	}
	cfg, state, err := s.store.Snapshot()
	if err != nil || configDigest(cfg, state) != preview.Digest {
		problem(w, 409, "配置已有变化，请重新预览")
		return
	}
	if len(preview.Missing) > 0 && !input.AcceptMissing {
		problem(w, 400, "请先确认将移除的节点")
		return
	}
	if err = s.commitImported(r.Context(), cfg, state, preview.Nodes, preview.URL); err != nil {
		s.commitFailure(w, err)
		return
	}
	reply(w, 200, map[string]any{"bound": true, "nodeCount": len(preview.Nodes)})
}
func (s *Server) commitImported(ctx context.Context, cfg map[string]any, state State, nodes []any, source string) error {
	total := len(nodes)
	for _, sub := range state.Subscriptions {
		total += sub.NodeCount
	}
	if state.Imported == nil && len(state.Subscriptions) >= 10 {
		return errors.New("最多保存 10 份订阅")
	}
	if total > 2000 {
		return errors.New("节点数量超过上限")
	}
	applyImportedNodes(cfg, &state, nodes)
	state.Imported = &Subscription{ID: "imported", Name: "已导入配置的订阅", URL: source, NodeCount: len(nodes), UpdatedAt: time.Now().Unix()}
	return s.commitConfig(ctx, cfg, state, s.options.Validate, func(ctx context.Context) error {
		_, current, e := s.store.Snapshot()
		if e != nil {
			return e
		}
		return s.fullReload(ctx, current)
	})
}
func (s *Server) updateImported(w http.ResponseWriter, r *http.Request) {
	cfg, state, err := s.store.Snapshot()
	if err != nil || state.Imported == nil {
		problem(w, 404, "尚未绑定原订阅")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	nodes, err := s.fetchSource(ctx, state.Imported.URL)
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	old, _ := cfg["proxies"].([]any)
	if len(missingNames(old, nodes)) > 0 {
		problem(w, 409, "更新将移除旧节点，请先重新预览并确认")
		return
	}
	if err = s.commitImported(ctx, cfg, state, nodes, state.Imported.URL); err != nil {
		s.commitFailure(w, err)
		return
	}
	reply(w, 200, map[string]any{"subscription": publicSubscription(*state.Imported)})
}
