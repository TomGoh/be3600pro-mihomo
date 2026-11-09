package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type concurrentService struct {
	mu sync.Mutex
	on bool
}

func (s *concurrentService) Enabled() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.on }
func (s *concurrentService) SetEnabled(ctx context.Context, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on = on
	return nil
}
func (s *concurrentService) StartCore(context.Context) error { return nil }
func (s *concurrentService) StopCore(context.Context) error  { return nil }

func TestEmergencyBypassesMutationAndFencesOldEnable(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	service := &concurrentService{on: true}
	s.options.Service = service
	entered, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan struct{})
	var oldEnableError error
	go func() {
		defer close(oldDone)
		s.mutate(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			oldEnableError = s.setEnabled(r.Context(), true)
		})(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	}()
	<-entered
	response := apiRequest(s, "POST", "/api/emergency-stop", `{}`, true)
	if response.Code != 200 {
		close(release)
		<-oldDone
		t.Fatalf("stop status=%d body=%s", response.Code, response.Body.String())
	}
	if service.Enabled() {
		t.Error("emergency did not disable")
	}
	close(release)
	<-oldDone
	if oldEnableError == nil || service.Enabled() {
		t.Fatal("old mutation resurrected proxy")
	}
}

func TestEmergencyCancelsActiveMutation(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	s.options.Service = &concurrentService{on: true}
	entered, finished := make(chan struct{}), make(chan struct{})
	go s.mutate(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(finished) })(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	<-entered
	if r := apiRequest(s, "POST", "/api/enabled", `{"enabled":false}`, true); r.Code != 200 {
		t.Fatalf("off status=%d", r.Code)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("mutation not cancelled")
	}
}

func TestEmergencyRequiresAuthentication(t *testing.T) {
	s, _, service, _ := serverFixture(t)
	if r := apiRequest(s, "POST", "/api/emergency-stop", `{}`, false); r.Code != 401 || !service.Enabled() {
		t.Fatalf("status=%d enabled=%v", r.Code, service.Enabled())
	}
}

func TestQueuedEnableCannotOvertakeEmergency(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	service := &concurrentService{on: true}
	s.options.Service = service
	s.mutation.Lock()
	queued := make(chan *httptest.ResponseRecorder, 1)
	seen := make(chan struct{})
	request := httptest.NewRequest("POST", "/api/enabled", strings.NewReader(`{"enabled":true}`))
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	request = request.WithContext(&admissionContext{Context: request.Context(), seen: seen})
	go func() { response := httptest.NewRecorder(); s.ServeHTTP(response, request); queued <- response }()
	<-seen
	if r := apiRequest(s, "POST", "/api/emergency-stop", `{}`, true); r.Code != 200 {
		s.mutation.Unlock()
		t.Fatalf("stop=%d", r.Code)
	}
	s.mutation.Unlock()
	r := <-queued
	if r.Code != 409 || service.Enabled() {
		t.Fatalf("queued enable status=%d enabled=%v", r.Code, service.Enabled())
	}
}
func TestDetachedRecoveryCannotEnableAfterEmergency(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	service := &concurrentService{on: true}
	s.options.Service = service
	ctx := context.WithValue(context.Background(), operationKey{}, operationIntent{0, ""})
	if r := apiRequest(s, "POST", "/api/emergency-stop", `{}`, true); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if err := s.setEnabled(context.WithoutCancel(ctx), true); err == nil || service.Enabled() {
		t.Fatal("detached recovery ignored emergency generation")
	}
}

type admissionContext struct {
	context.Context
	seen chan struct{}
	once sync.Once
}

func (c *admissionContext) Value(key any) any {
	if _, ok := key.(queuedIntentKey); ok {
		c.once.Do(func() { close(c.seen) })
	}
	return c.Context.Value(key)
}

func TestClientIntentCapturedBeforeStopCannotEnableAfterStop(t *testing.T) {
	s, _, service, root := serverFixture(t)
	os.WriteFile(filepath.Join(root, "intent-generation"), []byte("after-stop"), 0600)
	service.enabled = false
	r := httptest.NewRequest("POST", "/api/enabled", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	r.Header.Set("X-Intent-Epoch", "before-stop")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 || service.enabled {
		t.Fatalf("stale client enabled: %d", w.Code)
	}
}

type delayedBody struct {
	io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *delayedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Reader.Read(p)
}
func (b *delayedBody) Close() error { return nil }
func TestSlowLegacyEnableBodyCannotAdoptPostEmergencyGeneration(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	service := &concurrentService{on: true}
	s.options.Service = service
	body := &delayedBody{Reader: strings.NewReader(`{"enabled":true}`), entered: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest("POST", "/api/enabled", nil)
	request.Body = body
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); s.ServeHTTP(w, request); done <- w }()
	<-body.entered
	if r := apiRequest(s, "POST", "/api/emergency-stop", `{}`, true); r.Code != 200 {
		t.Fatal(r.Code)
	}
	close(body.release)
	w := <-done
	if w.Code != 409 || service.Enabled() {
		t.Fatalf("slow old request re-enabled: status=%d enabled=%v", w.Code, service.Enabled())
	}
}

type startupRecoveryService struct {
	concurrentService
	offCalls int
}

func (s *startupRecoveryService) SetEnabled(ctx context.Context, on bool) error {
	if !on {
		s.offCalls++
	}
	return s.concurrentService.SetEnabled(ctx, on)
}
func TestInterruptedStartupPersistsOffIntentBeforeRecovery(t *testing.T) {
	s, _, _, root := serverFixture(t)
	cfg, state, _ := s.store.Snapshot()
	writeTestJSON(t, filepath.Join(root, "transaction.json"), map[string]any{"config": cfg, "state": state})
	service := &startupRecoveryService{concurrentService: concurrentService{on: true}}
	options := s.options
	options.Service = service
	if _, err := NewServer(options); err != nil {
		t.Fatal(err)
	}
	if service.offCalls != 1 || service.Enabled() {
		t.Fatal("interrupted recovery only stopped runtime without persisting off intent")
	}
}
