package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"
)

type jobView struct {
	ID       string          `json:"id"`
	State    string          `json:"state"`
	Created  int64           `json:"createdAt"`
	Finished int64           `json:"finishedAt,omitempty"`
	Code     int             `json:"code,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
}
type jobRecord struct {
	view   jobView
	key    string
	digest [32]byte
}
type jobStore struct {
	sync.Mutex
	entries map[string]*jobRecord
}

var jobKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)

type queuedIntentKey struct{}

type jobInput struct {
	Key    string `json:"key"`
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
}

func (s *Server) submitJob(w http.ResponseWriter, r *http.Request) {
	var input jobInput
	if decodeBody(w, r, &input) != nil || !jobKey.MatchString(input.Key) {
		problem(w, 400, "任务参数无效")
		return
	}
	var handler http.HandlerFunc
	method, path := "POST", "/api/subscriptions"
	payload := map[string]string{}
	switch input.Action {
	case "subscription-add":
		handler = s.addSubscription
		payload = map[string]string{"name": input.Name, "url": input.URL}
	case "subscription-update":
		handler = s.updateSubscription
	case "subscription-delete":
		handler = s.deleteSubscription
		method = "DELETE"
	default:
		problem(w, 400, "任务类型无效")
		return
	}
	if input.Action != "subscription-add" && !subscriptionID.MatchString(input.ID) && input.ID != "imported" {
		problem(w, 400, "订阅标识无效")
		return
	}
	raw, _ := json.Marshal(input)
	digest := sha256.Sum256(raw)
	s.intentMu.Lock()
	intent := operationIntent{s.generation, readIntentEpoch(s.options.Root)}
	if arrival, ok := r.Context().Value(arrivalIntentKey{}).(operationIntent); ok {
		intent = arrival
	}
	s.intentMu.Unlock()
	if !requestEpochMatches(r, intent.epoch) {
		problem(w, 409, "操作意图已过期，请刷新状态后重试")
		return
	}
	s.jobs.Lock()
	if s.jobs.entries == nil {
		s.jobs.entries = map[string]*jobRecord{}
	}
	active := 0
	for id, record := range s.jobs.entries {
		if record.view.Finished > 0 && time.Now().Unix()-record.view.Finished > 3600 {
			delete(s.jobs.entries, id)
			continue
		}
		if record.key == input.Key {
			if record.digest != digest {
				s.jobs.Unlock()
				problem(w, 409, "同一任务标识不能用于不同操作")
				return
			}
			v := record.view
			s.jobs.Unlock()
			reply(w, 202, map[string]any{"job": v})
			return
		}
		if record.view.Finished == 0 {
			active++
		}
	}
	if len(s.jobs.entries) >= 32 && active < 8 {
		oldest := ""
		var at int64
		for id, r := range s.jobs.entries {
			if r.view.Finished > 0 && (oldest == "" || r.view.Finished < at) {
				oldest = id
				at = r.view.Finished
			}
		}
		if oldest != "" {
			delete(s.jobs.entries, oldest)
		}
	}
	if active >= 8 || len(s.jobs.entries) >= 32 {
		s.jobs.Unlock()
		problem(w, 429, "任务队列已满，请稍后重试")
		return
	}
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		s.jobs.Unlock()
		problem(w, 500, "无法创建任务")
		return
	}
	id := hex.EncodeToString(entropy[:])
	record := &jobRecord{key: input.Key, digest: digest, view: jobView{ID: id, State: "queued", Created: time.Now().Unix()}}
	s.jobs.entries[id] = record
	view := record.view
	s.jobs.Unlock()

	body, _ := json.Marshal(payload)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, queuedIntentKey{}, intent)
		request, _ := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
		request.SetPathValue("id", input.ID)
		out := &jobResponse{header: http.Header{}}
		s.mutate(func(w http.ResponseWriter, r *http.Request) {
			s.jobs.Lock()
			record.view.State = "running"
			s.jobs.Unlock()
			handler(w, r)
		})(out, request)
		s.jobs.Lock()
		defer s.jobs.Unlock()
		record.view.Code = out.code
		record.view.Result = append([]byte(nil), out.body.Bytes()...)
		record.view.Finished = time.Now().Unix()
		record.view.State = "succeeded"
		if out.code >= 400 {
			record.view.State = "failed"
		}
		if out.header.Get("X-Operation-Cancelled") == "true" {
			record.view.State = "cancelled"
		}
	}()
	reply(w, 202, map[string]any{"job": view})
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	s.jobs.Lock()
	record := s.jobs.entries[r.PathValue("id")]
	if record == nil || (record.view.Finished > 0 && time.Now().Unix()-record.view.Finished > 3600) {
		s.jobs.Unlock()
		problem(w, 404, "任务不存在或已过期")
		return
	}
	v := record.view
	s.jobs.Unlock()
	reply(w, 200, map[string]any{"job": v})
}

type jobResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *jobResponse) Header() http.Header { return w.header }
func (w *jobResponse) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *jobResponse) Write(data []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	if w.body.Len()+len(data) > 64*1024 {
		return len(data), nil
	}
	return w.body.Write(data)
}

func (s *Server) getJobByKey(w http.ResponseWriter, r *http.Request) {
	s.jobs.Lock()
	defer s.jobs.Unlock()
	for _, record := range s.jobs.entries {
		if record.key == r.PathValue("key") && (record.view.Finished == 0 || time.Now().Unix()-record.view.Finished <= 3600) {
			reply(w, 200, map[string]any{"job": record.view})
			return
		}
	}
	problem(w, 404, "任务不存在或已过期，请检查订阅后再提交")
}
