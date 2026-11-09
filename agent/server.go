package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	previews      previewStore
	failoverState map[string]*failoverRuntime
	jobs          jobStore
	health        healthState
	intentMu      sync.Mutex
	generation    uint64
	activeCancel  context.CancelFunc
	emergencyMu   sync.Mutex
	options       Options
	store         *Store
	handler       http.Handler
	mutation      sync.Mutex
	busy          atomic.Bool
	errorMu       sync.RWMutex
	lastError     string
}

func NewServer(options Options) (*Server, error) {
	if len(options.Token) < 16 || options.Core == nil || options.Service == nil {
		return nil, errors.New("management credentials and dependencies are required")
	}
	if err := os.MkdirAll(options.RunDir, 0700); err != nil {
		return nil, err
	}
	_, journalErr := os.Stat(filepath.Join(options.Root, "transaction.json"))
	recovered := journalErr == nil
	if recovered {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := options.Service.StopCore(ctx)
		if err == nil {
			err = options.Service.SetEnabled(ctx, false)
		}
		cancel()
		if err != nil {
			return nil, errors.New("interrupted transaction requires stopping old runtime")
		}
	}
	if err := RecoverStore(options.Root); err != nil {
		return nil, err
	}
	s := &Server{options: options, store: &Store{Root: options.Root}}
	_, state, err := s.store.Snapshot()
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(options.Root, "state.json")); os.IsNotExist(err) {
		data, _ := json.Marshal(state)
		if err = atomicWrite(filepath.Join(options.Root, "state.json"), data); err != nil {
			return nil, err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/jobs", s.submitJob)
	mux.HandleFunc("GET /api/jobs/by-key/{key}", s.getJobByKey)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/diagnostics", s.diagnostics)
	mux.HandleFunc("GET /api/backup", s.backupStatus)
	mux.HandleFunc("POST /api/backup", s.mutate(s.createBackup))
	mux.HandleFunc("POST /api/backup/restore", s.mutate(s.restoreBackup))
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/enabled", s.enabledDispatch)
	mux.HandleFunc("POST /api/emergency-stop", s.emergencyStop)
	mux.HandleFunc("POST /api/bootstrap-retry", s.mutate(s.bootstrapRetry))
	mux.HandleFunc("GET /api/failover", s.getFailover)
	mux.HandleFunc("PUT /api/failover", s.mutate(s.putFailover))
	mux.HandleFunc("GET /api/mode", s.getMode)
	mux.HandleFunc("PUT /api/mode", s.mutate(s.setMode))
	mux.HandleFunc("GET /api/groups", s.groups)
	mux.HandleFunc("PUT /api/groups/{group}", s.mutate(s.selectNode))
	mux.HandleFunc("POST /api/imported/preview", s.mutate(s.previewImported))
	mux.HandleFunc("POST /api/imported/apply", s.mutate(s.applyImported))
	mux.HandleFunc("GET /api/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /api/subscriptions", s.mutate(s.addSubscription))
	mux.HandleFunc("POST /api/subscriptions/{id}/update", s.mutate(s.updateSubscription))
	mux.HandleFunc("DELETE /api/subscriptions/{id}", s.mutate(s.deleteSubscription))
	mux.HandleFunc("POST /api/core", s.mutate(s.uploadCore))
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("PUT /api/devices/bypass", s.mutate(s.setDeviceBypassList))
	mux.HandleFunc("PUT /api/devices/{mac}/bypass", s.mutate(s.setDeviceBypass))
	s.handler = AuthMiddleware(options.Token, mux)
	if recovered {
		s.lastError = "上次更新中断，已恢复旧配置；代理保持关闭"
		if _, err = os.Stat(options.CorePath); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			_ = options.Service.StartCore(ctx)
			cancel()
		}
	}
	return s, nil
}

type arrivalIntentKey struct{}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.intentMu.Lock()
	intent := operationIntent{s.generation, readIntentEpoch(s.options.Root)}
	s.intentMu.Unlock()
	w.Header().Set("X-Intent-Epoch", intent.epoch)
	s.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), arrivalIntentKey{}, intent)))
}

func (s *Server) mutate(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.intentMu.Lock()
		generation := s.generation
		epoch := readIntentEpoch(s.options.Root)
		if arrival, ok := r.Context().Value(arrivalIntentKey{}).(operationIntent); ok {
			generation = arrival.generation
			epoch = arrival.epoch
		}
		if queued, ok := r.Context().Value(queuedIntentKey{}).(operationIntent); ok {
			generation = queued.generation
			epoch = queued.epoch
		}
		s.intentMu.Unlock()
		if !requestEpochMatches(r, epoch) {
			w.Header().Set("X-Operation-Cancelled", "true")
			problem(w, 409, "操作意图已过期，请刷新状态后重试")
			return
		}
		s.mutation.Lock()
		defer s.mutation.Unlock()
		s.intentMu.Lock()
		if generation != s.generation || epoch != readIntentEpoch(s.options.Root) || r.Context().Err() != nil {
			s.intentMu.Unlock()
			w.Header().Set("X-Operation-Cancelled", "true")
			problem(w, 409, "操作已被急停取消，请重新操作")
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		ctx = context.WithValue(ctx, operationKey{}, operationIntent{generation, epoch})
		s.activeCancel = cancel
		s.intentMu.Unlock()
		defer func() { cancel(); s.intentMu.Lock(); s.activeCancel = nil; s.intentMu.Unlock() }()
		s.busy.Store(true)
		defer s.busy.Store(false)
		handler(w, r.WithContext(ctx))
	}
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func decodeBody(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing request")
	}
	return nil
}
func (s *Server) recordError(message string) {
	s.errorMu.Lock()
	s.lastError = message
	s.errorMu.Unlock()
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	version, err := s.options.Core.Request(ctx, "GET", "/version", nil)
	running := err == nil
	var versionData map[string]any
	_ = json.Unmarshal(version, &versionData)
	cfg, _, _ := s.store.Snapshot()
	mode, _ := cfg["mode"].(string)
	s.errorMu.RLock()
	last := s.lastError
	s.errorMu.RUnlock()
	if !running && last == "" {
		last = "内核尚未就绪，可使用离线恢复"
	}
	if text, e := os.ReadFile(filepath.Join(s.options.RunDir, "bootstrap-error")); e == nil && !running {
		last = strings.TrimSpace(string(text))
	}
	scope := "lan"
	if data, _ := os.ReadFile(filepath.Join(s.options.RunDir, "scope")); len(strings.TrimSpace(string(data))) > 0 {
		scope = "test-device"
	}
	free, _ := availableBytes(s.options.Root)
	memAvailable := int64(0)
	if data, e := os.ReadFile("/proc/meminfo"); e == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[0] == "MemAvailable:" {
				memAvailable, _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
	reply(w, 200, map[string]any{"coreRunning": running, "enabled": s.options.Service.Enabled(), "mode": mode, "version": versionData["version"], "busy": s.busy.Load(), "scope": scope, "error": last, "memoryAvailableKiB": memAvailable, "flashFreeBytes": free, "memoryCore": true, "health": s.healthView(), "bootstrapStage": readPublicStage(s.options.RunDir)})
}

func (s *Server) enabled(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if decodeBody(w, r, &input) != nil || input.Enabled == nil {
		problem(w, 400, "请提供代理开关状态")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.setEnabled(ctx, *input.Enabled); err != nil {
		s.recordError("代理开关操作失败，请检查核心和节点状态")
		problem(w, 503, "代理开关操作失败，请检查核心和节点状态")
		return
	}
	s.recordError("")
	reply(w, 200, map[string]bool{"enabled": s.options.Service.Enabled()})
}

func (s *Server) getMode(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := s.store.Snapshot()
	if err != nil {
		problem(w, 500, "无法读取当前模式")
		return
	}
	reply(w, 200, map[string]any{"mode": cfg["mode"]})
}
func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode string `json:"mode"`
	}
	if decodeBody(w, r, &input) != nil || (input.Mode != "rule" && input.Mode != "global" && input.Mode != "direct") {
		problem(w, 400, "模式须为规则、全局或直连")
		return
	}
	cfg, state, err := s.store.Snapshot()
	if err != nil {
		problem(w, 500, "配置读取失败")
		return
	}
	cfg["mode"] = input.Mode
	reload := func(ctx context.Context) error {
		current, _, e := s.store.Snapshot()
		if e != nil {
			return e
		}
		_, e = s.options.Core.Request(ctx, "PATCH", "/configs", map[string]any{"mode": current["mode"]})
		return e
	}
	if err = s.commitConfig(r.Context(), cfg, state, s.options.Validate, reload); err != nil {
		s.commitFailure(w, err)
		return
	}
	s.recordError("")
	reply(w, 200, map[string]string{"mode": input.Mode})
}

type proxyInfo struct {
	Type    string   `json:"type"`
	Now     string   `json:"now"`
	All     []string `json:"all"`
	History []struct {
		Delay int `json:"delay"`
	} `json:"history"`
}

func (s *Server) proxyMap(ctx context.Context) (map[string]proxyInfo, error) {
	data, err := s.options.Core.Request(ctx, "GET", "/proxies", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Proxies map[string]proxyInfo `json:"proxies"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response.Proxies, nil
}
func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	proxies, err := s.proxyMap(r.Context())
	if err != nil {
		problem(w, 503, "核心尚未就绪，暂时无法读取节点")
		return
	}
	names := []string{}
	for name, p := range proxies {
		if !strings.HasPrefix(name, "__cdx_") && (p.Type == "Selector" || p.Type == "URLTest" || p.Type == "Fallback" || p.Type == "LoadBalance") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	groups := []any{}
	for _, name := range names {
		p := proxies[name]
		candidates := []any{}
		for _, n := range p.All {
			if strings.HasPrefix(n, "__cdx_") {
				continue
			}
			delay := 0
			if history := proxies[n].History; len(history) > 0 {
				delay = history[len(history)-1].Delay
			}
			candidates = append(candidates, map[string]any{"name": n, "delay": delay, "type": proxies[n].Type})
		}
		groups = append(groups, map[string]any{"name": name, "type": p.Type, "current": p.Now, "selectable": p.Type == "Selector", "candidates": candidates})
	}
	reply(w, 200, map[string]any{"groups": groups})
}

func (s *Server) selectNode(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("group")
	var input struct {
		Name string `json:"name"`
	}
	if decodeBody(w, r, &input) != nil || input.Name == "" || strings.HasPrefix(group, "__cdx_") || strings.HasPrefix(input.Name, "__cdx_") {
		problem(w, 400, "节点选择无效")
		return
	}
	proxies, err := s.proxyMap(r.Context())
	if err != nil {
		problem(w, 503, "无法读取策略组")
		return
	}
	p, exists := proxies[group]
	valid := false
	for _, n := range p.All {
		if n == input.Name {
			valid = true
		}
	}
	if !exists || p.Type != "Selector" || !valid {
		problem(w, 400, "节点不在当前策略组候选列表中")
		return
	}
	if err = s.disableManualFailover(group); err != nil {
		problem(w, 500, "无法停用自动切换，节点未改变")
		return
	}
	if _, err = s.options.Core.Request(r.Context(), "PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": input.Name}); err != nil {
		problem(w, 502, "核心未能切换节点")
		return
	}
	_, state, err := s.store.Snapshot()
	if err == nil {
		state.Selections[group] = input.Name
		data, _ := json.Marshal(state)
		err = s.store.requireSpace(uint64(len(data) * 2))
		if err == nil {
			err = atomicWrite(filepath.Join(s.options.Root, "state.json"), data)
		}
	}
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.options.Core.Request(ctx, "PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": p.Now})
		problem(w, 500, "选择未能保存，已恢复原节点")
		return
	}
	reply(w, 200, map[string]string{"name": input.Name})
}

func (s *Server) restoreSelections(ctx context.Context, state State) error {
	proxies, err := s.proxyMap(ctx)
	if err != nil {
		return err
	}
	for group, node := range state.Selections {
		p, ok := proxies[group]
		if !ok || p.Type != "Selector" {
			continue
		}
		for _, n := range p.All {
			if n == node {
				if _, err = s.options.Core.Request(ctx, "PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": node}); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
func (s *Server) fullReload(ctx context.Context, state State) error {
	_, err := s.options.Core.Request(ctx, "PUT", "/configs", map[string]string{"path": filepath.Join(s.options.Root, "config.json")})
	if err == nil {
		err = s.restoreSelections(ctx, state)
	}
	return err
}
func (s *Server) commitFailure(w http.ResponseWriter, err error) {
	message := "更新失败，已保留上一次可用配置"
	if errors.Is(err, ErrRollback) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = s.setEnabled(ctx, false)
		cancel()
		message = "配置恢复未完成，代理已回退直连；请通过 SSH 重启管理服务恢复"
	}
	s.recordError(message)
	problem(w, 500, message)
}

func (s *Server) bootstrapRetry(w http.ResponseWriter, r *http.Request) {
	service, ok := s.options.Service.(*ScriptService)
	if !ok {
		problem(w, 503, "恢复服务不可用")
		return
	}
	if err := service.run(r.Context(), "bootstrap-retry"); err != nil {
		problem(w, 503, "恢复任务未能启动")
		return
	}
	reply(w, 202, map[string]bool{"started": true})
}
