package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu    sync.Mutex
	items []Confession
	views map[string]map[string]struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{views: make(map[string]map[string]struct{})}
}

func (s *fakeStore) List(_ context.Context, _, category string, limit int) ([]Confession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Confession, 0, len(s.items))
	for _, item := range s.items {
		if category != "" && category != "все" && category != item.Category {
			continue
		}
		item.Views = len(s.views[item.ID])
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *fakeStore) Create(_ context.Context, text, category, _ string, now time.Time) (Confession, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := Confession{ID: newID(), Text: text, Category: category, CreatedAt: now}
	s.items = append(s.items, item)
	return item, 0, nil
}

func (s *fakeStore) RecordView(_ context.Context, id, ipHash string, _ time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, item := range s.items {
		if item.ID == id {
			found = true
			break
		}
	}
	if !found {
		return 0, ErrNotFound
	}
	if s.views[id] == nil {
		s.views[id] = make(map[string]struct{})
	}
	s.views[id][ipHash] = struct{}{}
	return len(s.views[id]), nil
}

func (s *fakeStore) Stats(_ context.Context) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	totalViews := 0
	for _, viewers := range s.views {
		totalViews += len(viewers)
	}
	return map[string]int{"confessions": len(s.items), "views": totalViews}, nil
}

func testApplication(store confessionStore, now func() time.Time, requests, burst int) *application {
	return &application{
		store:             store,
		salt:              "test-salt-that-is-long-enough-for-tests",
		limiter:           NewIPRateLimiter(requests, time.Minute, burst),
		rateLimitRequests: requests,
		now:               now,
	}
}

func TestAPIFlow(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	app := testApplication(store, func() time.Time { return now }, 100, 100)
	handler := app.routes()

	payload, _ := json.Marshal(createRequest{Text: "Я давно хотел честно рассказать об этом поступке.", Category: "поступок"})
	request := httptest.NewRequest(http.MethodPost, "/api/confessions", bytes.NewReader(payload))
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", recorder.Code)
	}
	if got := response.Header.Get("X-RateLimit-Limit"); got != "100" {
		t.Fatalf("expected rate limit header, got %q", got)
	}

	var created Confession
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		viewRequest := httptest.NewRequest(http.MethodPost, "/api/confessions/"+created.ID+"/view", nil)
		viewRequest.RemoteAddr = "192.0.2.2:5678"
		viewRecorder := httptest.NewRecorder()
		handler.ServeHTTP(viewRecorder, viewRequest)
		var result map[string]int
		if err := json.NewDecoder(viewRecorder.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result["views"] != 1 {
			t.Fatalf("expected one unique view, got %d", result["views"])
		}
	}
}

func TestAPIRateLimiter(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	app := testApplication(newFakeStore(), func() time.Time { return now }, 60, 2)
	handler := app.routes()

	for i, expected := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
		request.RemoteAddr = "192.0.2.50:1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != expected {
			t.Fatalf("request %d: expected %d, got %d", i+1, expected, recorder.Code)
		}
		if expected == http.StatusTooManyRequests && recorder.Header().Get("Retry-After") == "" {
			t.Fatal("expected Retry-After header")
		}
	}
}

func TestLimiterRefills(t *testing.T) {
	limiter := NewIPRateLimiter(60, time.Minute, 1)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if allowed, _ := limiter.Allow("ip", now); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _ := limiter.Allow("ip", now); allowed {
		t.Fatal("burst should be exhausted")
	}
	if allowed, _ := limiter.Allow("ip", now.Add(time.Second)); !allowed {
		t.Fatal("one token should refill after one second")
	}
}
