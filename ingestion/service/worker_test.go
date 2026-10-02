package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/ingestion/config"
	"github.com/gaucho-racing/blix/ingestion/model"
	"github.com/gaucho-racing/blix/ingestion/pkg/identifier"
)

func TestPageConcurrencyIsBoundedAcrossDocuments(t *testing.T) {
	worker := NewWorker(config.Config{DocumentConcurrency: 2, PageConcurrency: 4, GlobalPageConcurrency: 3})
	var active, maximum, processed atomic.Int64
	process := func(ctx context.Context, number int) error {
		now := active.Add(1)
		for prev := maximum.Load(); now > prev; prev = maximum.Load() {
			if maximum.CompareAndSwap(prev, now) {
				break
			}
		}
		defer active.Add(-1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		processed.Add(1)
		return nil
	}
	var tasks sync.WaitGroup
	for range 2 {
		tasks.Go(func() {
			if err := worker.processPages(context.Background(), 10, map[int]bool{1: true, 3: true}, process); err != nil {
				t.Error(err)
			}
		})
	}
	tasks.Wait()
	if maximum.Load() != 3 || processed.Load() != 16 {
		t.Fatalf("global limit or resume broken: max=%d processed=%d", maximum.Load(), processed.Load())
	}
}
func TestPageFailureCancelsSiblingWork(t *testing.T) {
	worker := NewWorker(config.Config{DocumentConcurrency: 1, PageConcurrency: 4, GlobalPageConcurrency: 4})
	failure := errors.New("render failed")
	var stopped atomic.Int64
	err := worker.processPages(context.Background(), 100, nil, func(ctx context.Context, number int) error {
		if number == 1 {
			return failure
		}
		<-ctx.Done()
		stopped.Add(1)
		return ctx.Err()
	})
	if !errors.Is(err, failure) || len(worker.pages) != 0 {
		t.Fatalf("failure was not propagated or slots leaked: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.processPages(ctx, 1, nil, func(context.Context, int) error { t.Fatal("cancelled work started"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type fixtureProcessor struct{ pages atomic.Int64 }

func (*fixtureProcessor) Extract(context.Context, string) ([]string, error) {
	return []string{"native one", "native two"}, nil
}
func (f *fixtureProcessor) Page(ctx context.Context, path string, number int, native string) (model.Page, error) {
	f.pages.Add(1)
	return model.Page{NativeText: native, Text: native, TextMethod: "native_text", Image: []byte("image"), SHA256: "image-hash", Width: 10, Height: 20}, nil
}
func TestExtractionResumesCheckpointsAndCompletesBeforeRuntime(t *testing.T) {
	docID := identifier.New(identifier.Document)
	var saved atomic.Int64
	var completed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal" {
			t.Error("missing service authentication")
		}
		switch r.URL.Path {
		case "/internal/v1/documents/" + docID + "/extraction/prepare":
			json.NewEncoder(w).Encode(model.Manifest{MediaType: "application/pdf", Version: 1, CompletedPages: []int{1}})
		case "/internal/v1/documents/" + docID + "/extraction/file":
			w.Write([]byte("%PDF-fixture"))
		case "/internal/v1/documents/" + docID + "/extraction/start":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request["page_count"] != float64(2) || request["generation"] != float64(1) {
				t.Error("invalid extraction start")
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "/internal/v1/documents/" + docID + "/extraction/pages/2":
			var request model.Page
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Text != "native two" || request.ImageBase64 == "" || request.FencingToken != "fence" {
				t.Error("invalid page callback")
			}
			saved.Add(1)
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "/internal/v1/documents/" + docID + "/extraction/complete":
			if saved.Load() != 1 {
				t.Error("completed before saving pages")
			}
			completed.Store(true)
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	worker := NewWorker(config.Config{KnowledgeURL: server.URL, InternalToken: "internal", DocumentConcurrency: 1, PageConcurrency: 2, GlobalPageConcurrency: 2})
	p := &fixtureProcessor{}
	worker.processors["application/pdf"] = p
	var task claim
	task.Attempt.ID = "attempt"
	task.Attempt.FencingToken = "fence"
	task.Task.Input.DocumentID = docID
	task.Task.Input.Generation = 1
	output, err := worker.extract(context.Background(), task)
	if err != nil || output["page_count"] != 2 || p.pages.Load() != 1 || !completed.Load() {
		t.Fatalf("checkpoint resume failed: %+v %v", output, err)
	}
}
func TestHTTPClientFailsClosedOnRedirect(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	err := NewClient(redirect.URL, "secret", "test").request(context.Background(), "POST", "callback", map[string]bool{"ok": true}, nil)
	var failure *HTTPError
	if !errors.As(err, &failure) || failure.Status != http.StatusTemporaryRedirect || followed.Load() {
		t.Fatalf("redirect followed or accepted: %v", err)
	}
}

type blockingProcessor struct {
	started chan struct{}
	stopped atomic.Bool
}

func (*blockingProcessor) Extract(context.Context, string) ([]string, error) {
	return []string{"page"}, nil
}
func (p *blockingProcessor) Page(ctx context.Context, path string, number int, native string) (model.Page, error) {
	close(p.started)
	<-ctx.Done()
	p.stopped.Store(true)
	return model.Page{}, ctx.Err()
}
func TestLostLeaseStopsExtractionBeforePublishing(t *testing.T) {
	docID := identifier.New(identifier.Document)
	p := &blockingProcessor{started: make(chan struct{})}
	var completed atomic.Bool
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/prepare"):
			json.NewEncoder(w).Encode(model.Manifest{MediaType: "application/pdf", Version: 1})
		case strings.HasSuffix(r.URL.Path, "/file"):
			w.Write([]byte("%PDF-fixture"))
		case strings.HasSuffix(r.URL.Path, "/start"):
			w.Write([]byte("{}"))
		default:
			completed.Store(true)
			w.Write([]byte("{}"))
		}
	}))
	defer knowledge.Close()
	runway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			select {
			case <-p.started:
			case <-r.Context().Done():
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/complete") {
			completed.Store(true)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer runway.Close()
	worker := NewWorker(config.Config{KnowledgeURL: knowledge.URL, RunwayURL: runway.URL, InternalToken: "internal", DocumentConcurrency: 1, PageConcurrency: 1, GlobalPageConcurrency: 1})
	worker.processors["application/pdf"] = p
	var task claim
	task.Attempt.ID = "attempt"
	task.Attempt.FencingToken = "fence"
	task.Attempt.DeadlineAt = time.Now().Add(time.Second)
	task.LeaseRenewAfterMS = 10
	task.Task.Input.DocumentID = docID
	task.Task.Input.Generation = 1
	worker.execute(context.Background(), task)
	if !p.stopped.Load() || completed.Load() {
		t.Fatal("lease loss did not stop processing before publication")
	}
}
func TestWorkerHeartbeatContinuesDuringSlowQueuePoll(t *testing.T) {
	var heartbeats atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			w.Write([]byte(`{"heartbeat_interval_ms":10}`))
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			w.Write([]byte("{}"))
		default:
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	worker := NewWorker(config.Config{RunwayURL: server.URL, DocumentConcurrency: 1, PageConcurrency: 1, GlobalPageConcurrency: 1, PollInterval: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	worker.Run(ctx)
	if heartbeats.Load() < 2 || worker.Ready() {
		t.Fatalf("queue poll blocked heartbeats or shutdown left ready=true: %d", heartbeats.Load())
	}
}
