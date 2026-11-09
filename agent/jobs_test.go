package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestJobsIdempotencyConflictAndCompletion(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	body := `{"key":"fixture-request-0001","action":"invalid"}`
	if r := apiRequest(s, "POST", "/api/jobs", body, true); r.Code != 400 {
		t.Fatalf("invalid=%d", r.Code)
	}
	body = `{"key":"fixture-request-0001","action":"subscription-update","id":"sub-0123456789ab"}`
	r := apiRequest(s, "POST", "/api/jobs", body, true)
	if r.Code != 202 {
		t.Fatalf("create=%d %s", r.Code, r.Body.String())
	}
	var first struct {
		Job jobView `json:"job"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &first)
	duplicate := apiRequest(s, "POST", "/api/jobs", body, true)
	var second struct {
		Job jobView `json:"job"`
	}
	_ = json.Unmarshal(duplicate.Body.Bytes(), &second)
	if first.Job.ID == "" || first.Job.ID != second.Job.ID {
		t.Fatal("retry duplicated job")
	}
	conflict := apiRequest(s, "POST", "/api/jobs", `{"key":"fixture-request-0001","action":"subscription-delete","id":"sub-0123456789ab"}`, true)
	if conflict.Code != 409 {
		t.Fatalf("conflict=%d", conflict.Code)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r = apiRequest(s, "GET", "/api/jobs/"+first.Job.ID, "", true)
		_ = json.Unmarshal(r.Body.Bytes(), &second)
		if second.Job.State == "failed" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish as failed")
}

func TestQueuedSubscriptionJobIsCancelledByEmergency(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	s.options.Service = &concurrentService{on: true}
	s.mutation.Lock()
	r := apiRequest(s, "POST", "/api/jobs", `{"key":"queued-before-emergency","action":"subscription-update","id":"sub-0123456789ab"}`, true)
	var result struct {
		Job jobView `json:"job"`
	}
	json.Unmarshal(r.Body.Bytes(), &result)
	if r.Code != 202 {
		s.mutation.Unlock()
		t.Fatal(r.Code)
	}
	stop := apiRequest(s, "POST", "/api/emergency-stop", `{}`, true)
	s.mutation.Unlock()
	if stop.Code != 200 {
		t.Fatal(stop.Code)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r = apiRequest(s, "GET", "/api/jobs/"+result.Job.ID, "", true)
		json.Unmarshal(r.Body.Bytes(), &result)
		if result.Job.State == "cancelled" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued job not cancelled: %+v", result.Job)
}

func TestJobQueueBoundAndExpiredHistory(t *testing.T) {
	s, _, _, _ := serverFixture(t)
	s.options.Service = &concurrentService{}
	s.mutation.Lock()
	ids := []string{}
	for i := 0; i < 8; i++ {
		body := fmt.Sprintf(`{"key":"bounded-request-%02d","action":"subscription-update","id":"sub-0123456789ab"}`, i)
		r := apiRequest(s, "POST", "/api/jobs", body, true)
		var result struct {
			Job jobView `json:"job"`
		}
		json.Unmarshal(r.Body.Bytes(), &result)
		if r.Code != 202 {
			s.mutation.Unlock()
			t.Fatal("queue rejected valid slot", r.Code)
		}
		ids = append(ids, result.Job.ID)
	}
	r := apiRequest(s, "POST", "/api/jobs", `{"key":"overflow-request-09","action":"subscription-update","id":"sub-0123456789ab"}`, true)
	if r.Code != 429 {
		s.mutation.Unlock()
		t.Fatal("unbounded active queue", r.Code)
	}
	apiRequest(s, "POST", "/api/emergency-stop", `{}`, true)
	s.mutation.Unlock()
	for _, id := range ids {
		deadline := time.Now().Add(time.Second)
		for {
			r = apiRequest(s, "GET", "/api/jobs/"+id, "", true)
			var result struct {
				Job jobView `json:"job"`
			}
			json.Unmarshal(r.Body.Bytes(), &result)
			if result.Job.Finished > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cancelled queue did not drain")
			}
			time.Sleep(time.Millisecond)
		}
	}
	s.jobs.Lock()
	s.jobs.entries[ids[0]].view.Finished = time.Now().Unix() - 3601
	s.jobs.Unlock()
	if r = apiRequest(s, "GET", "/api/jobs/"+ids[0], "", true); r.Code != 404 {
		t.Fatal("expired job remains visible", r.Code)
	}
}
