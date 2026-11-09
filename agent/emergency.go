package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type operationKey struct{}
type operationIntent struct {
	generation uint64
	epoch      string
}

func readIntentEpoch(root string) string {
	data, _ := os.ReadFile(filepath.Join(root, "intent-generation"))
	return strings.TrimSpace(string(data))
}

// A cancelled upload may still finish restoring the core file. It must never
// restore interception after a newer emergency, even using WithoutCancel.
func (s *Server) setEnabled(ctx context.Context, on bool) error {
	if on {
		s.intentMu.Lock()
		intent, ok := ctx.Value(operationKey{}).(operationIntent)
		stale := ok && intent.generation != s.generation
		s.intentMu.Unlock()
		if stale || ctx.Err() != nil {
			return errors.New("操作已被取消")
		}
	}
	return s.options.Service.SetEnabled(ctx, on)
}
func (s *Server) enabledDispatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if decodeBody(w, r, &input) != nil || input.Enabled == nil {
		problem(w, 400, "请提供代理开关状态")
		return
	}
	if !*input.Enabled {
		s.emergencyStop(w, r)
		return
	}
	r.Body = http.NoBody
	r.Body = io.NopCloser(bytes.NewBufferString(`{"enabled":true}`))
	s.mutate(s.enabled)(w, r)
}
func (s *Server) emergencyStop(w http.ResponseWriter, r *http.Request) {
	s.emergencyMu.Lock()
	defer s.emergencyMu.Unlock()
	s.intentMu.Lock()
	s.generation++
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.intentMu.Unlock()
	// Do not depend on a phone connection remaining open while restoring Internet.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := s.options.Service.SetEnabled(ctx, false); err != nil {
		s.recordError("急停未能完成，请使用 SSH 执行 routerctl.sh disable")
		problem(w, 503, "急停未能完成，请检查路由器状态")
		return
	}
	s.recordError("")
	reply(w, 200, map[string]any{"enabled": false, "cancelledPreviousOperations": true, "intentEpoch": readIntentEpoch(s.options.Root)})
}

func requestEpochMatches(r *http.Request, epoch string) bool {
	_, present := r.Header[http.CanonicalHeaderKey("X-Intent-Epoch")]
	return !present || r.Header.Get("X-Intent-Epoch") == epoch
}
