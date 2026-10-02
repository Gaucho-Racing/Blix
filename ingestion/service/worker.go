package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaucho-racing/blix/ingestion/config"
	"github.com/gaucho-racing/blix/ingestion/model"
	"github.com/gaucho-racing/blix/ingestion/pkg/depot"
	"github.com/gaucho-racing/blix/ingestion/pkg/identifier"
	"github.com/gaucho-racing/blix/ingestion/pkg/logger"
	"github.com/gaucho-racing/blix/ingestion/processor"
)

type Worker struct {
	config     config.Config
	id         string
	runway     *Client
	knowledge  *Client
	depot      *depot.Client
	processors map[string]processor.Processor
	pages      chan struct{}
	documents  chan struct{}
	ready      atomic.Bool
	active     atomic.Int64
}

func NewWorker(c config.Config) *Worker {
	return &Worker{config: c, id: identifier.New(identifier.Worker), runway: NewClient(c.RunwayURL, c.InternalToken, "Runway"), knowledge: NewClient(c.KnowledgeURL, c.InternalToken, "Knowledge"), depot: depot.NewClient(c.DepotURL, c.DepotBucket, c.SentinelServiceToken), processors: map[string]processor.Processor{"application/pdf": processor.PDF{}}, pages: make(chan struct{}, c.GlobalPageConcurrency), documents: make(chan struct{}, c.DocumentConcurrency)}
}
func (w *Worker) Ready() bool            { return w.ready.Load() }
func (w *Worker) ActiveDocuments() int64 { return w.active.Load() }

type claim struct {
	Attempt struct {
		ID           string    `json:"attempt_id"`
		RunID        string    `json:"run_id"`
		SessionID    string    `json:"session_id"`
		FencingToken string    `json:"fencing_token"`
		Version      uint64    `json:"version"`
		DeadlineAt   time.Time `json:"deadline_at"`
	} `json:"attempt"`
	Task struct {
		Input struct {
			DocumentID string `json:"document_id"`
			Generation uint64 `json:"generation"`
		} `json:"input"`
	} `json:"task"`
	LeaseRenewAfterMS uint64 `json:"lease_renew_after_ms"`
}

func (w *Worker) Run(ctx context.Context) {
	var tasks sync.WaitGroup
	defer func() { w.ready.Store(false); tasks.Wait() }()
	ticker := time.NewTicker(min(w.config.PollInterval, time.Second))
	defer ticker.Stop()
	nextPollAt := time.Time{}
	var heartbeatInterval atomic.Int64
	heartbeatInterval.Store(int64(5 * time.Second))
	tasks.Go(func() { w.heartbeat(ctx, &heartbeatInterval) })
	for ctx.Err() == nil {
		if !w.ready.Load() {
			var registered struct {
				HeartbeatIntervalMS uint64 `json:"heartbeat_interval_ms"`
			}
			err := w.runway.request(ctx, "POST", "workers/register", map[string]any{"worker_id": w.id, "runtime_kind": "ingestion", "concurrency": w.config.DocumentConcurrency, "background_concurrency": w.config.DocumentConcurrency}, &registered)
			if err != nil {
				w.report("register ingestion worker", err)
			} else {
				if registered.HeartbeatIntervalMS > 0 {
					heartbeatInterval.Store(int64(time.Duration(registered.HeartbeatIntervalMS) * time.Millisecond))
				}
				w.ready.Store(true)
			}
		}
		if w.ready.Load() && !time.Now().Before(nextPollAt) {
			nextPollAt = time.Now().Add(w.config.PollInterval)
			if err := w.poll(ctx, &tasks); err != nil {
				w.report("poll extraction tasks", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w *Worker) heartbeat(ctx context.Context, interval *atomic.Int64) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !w.ready.Load() || time.Since(last) < time.Duration(interval.Load()) {
			continue
		}
		if err := w.runway.request(ctx, "POST", "workers/"+w.id+"/heartbeat", nil, nil); err != nil {
			w.ready.Store(false)
			w.report("worker heartbeat", err)
		}
		last = time.Now()
	}
}
func (w *Worker) report(message string, err error) {
	if logger.SugarLogger != nil {
		logger.SugarLogger.Warnw(message, "error", err)
	}
}
func (w *Worker) poll(ctx context.Context, tasks *sync.WaitGroup) error {
	if len(w.documents) == cap(w.documents) {
		return nil
	}
	var ready struct {
		Attempts []struct {
			ID string `json:"attempt_id"`
		} `json:"attempts"`
	}
	if err := w.runway.request(ctx, "GET", "attempts/ready?runtime_kind=ingestion&limit=20", nil, &ready); err != nil {
		return err
	}
	for _, candidate := range ready.Attempts {
		select {
		case w.documents <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
		var task claim
		err := w.runway.request(ctx, "POST", "attempts/"+url.PathEscape(candidate.ID)+"/claim", map[string]string{"worker_id": w.id, "runtime_kind": "ingestion"}, &task)
		if err != nil {
			<-w.documents
			var failure *HTTPError
			if errors.As(err, &failure) && failure.Status == http.StatusConflict {
				continue
			}
			return err
		}
		w.active.Add(1)
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			defer func() { <-w.documents; w.active.Add(-1) }()
			w.execute(ctx, task)
		}()
	}
	return nil
}
func (w *Worker) ownership(task claim) map[string]any {
	return map[string]any{"worker_id": w.id, "fencing_token": task.Attempt.FencingToken, "expected_version": task.Attempt.Version}
}
func (w *Worker) execute(parent context.Context, task claim) {
	ctx, cancel := context.WithDeadline(parent, task.Attempt.DeadlineAt)
	done := make(chan struct{})
	interval := time.Duration(task.LeaseRenewAfterMS) * time.Millisecond
	if interval < time.Millisecond {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := w.runway.request(ctx, "POST", "attempts/"+task.Attempt.ID+"/heartbeat", w.ownership(task), nil); err != nil {
				w.report("extraction lease lost", err)
				cancel()
				return
			}
			var commands struct {
				Commands []struct {
					Kind string `json:"kind"`
				} `json:"commands"`
			}
			if err := w.runway.request(ctx, "POST", "attempts/"+task.Attempt.ID+"/commands", w.ownership(task), &commands); err != nil {
				w.report("extraction command polling", err)
				cancel()
				return
			}
			for _, command := range commands.Commands {
				if command.Kind == "cancel" {
					cancel()
					return
				}
			}
		}
	}()
	output, err := w.extract(ctx, task)
	cancel()
	<-done
	if parent.Err() != nil {
		return
	}
	reportCtx, stop := context.WithTimeout(parent, 15*time.Second)
	defer stop()
	if err != nil {
		w.report("extract document", err)
		failure := w.ownership(task)
		failure["error"] = map[string]string{"code": "file_extraction_failed", "message": err.Error()}
		retryable := !errors.Is(err, processor.ErrInvalidFile)
		var httpError *HTTPError
		if errors.As(err, &httpError) && (httpError.Status == http.StatusBadRequest || httpError.Status == http.StatusForbidden) {
			retryable = false
		}
		failure["retryable"] = retryable
		if reportErr := w.runway.request(reportCtx, "POST", "attempts/"+task.Attempt.ID+"/fail", failure, nil); reportErr != nil {
			w.report("report extraction failure", reportErr)
		}
		return
	}
	completion := w.ownership(task)
	completion["result"] = map[string]any{"run_id": task.Attempt.RunID, "session_id": task.Attempt.SessionID, "agent_id": "file_ingest_v1", "status": "succeeded", "output": output}
	if err := w.runway.request(reportCtx, "POST", "attempts/"+task.Attempt.ID+"/complete", completion, nil); err != nil {
		w.report("complete extraction task", err)
	}
}
func (w *Worker) extract(ctx context.Context, task claim) (map[string]any, error) {
	input := task.Task.Input
	if !identifier.IsValid(input.DocumentID, identifier.Document) || input.Generation == 0 {
		return nil, fmt.Errorf("%w: invalid document invocation", processor.ErrInvalidFile)
	}
	execution := model.Execution{AttemptID: task.Attempt.ID, WorkerID: w.id, FencingToken: task.Attempt.FencingToken, Generation: input.Generation}
	path := "documents/" + input.DocumentID + "/extraction/"
	var manifest model.Manifest
	if err := w.knowledge.request(ctx, "POST", path+"prepare", execution, &manifest); err != nil {
		return nil, err
	}
	p, ok := w.processors[manifest.MediaType]
	if !ok {
		return nil, fmt.Errorf("%w: unsupported media type %s", processor.ErrInvalidFile, manifest.MediaType)
	}
	if manifest.Version != model.ExtractionVersion {
		return nil, fmt.Errorf("%w: unsupported extraction version", processor.ErrInvalidFile)
	}
	var data []byte
	var err error
	if manifest.DepotFileID != "" {
		data, err = w.depot.Download(ctx, manifest.DepotFileID, model.MaxFileBytes)
	} else {
		var res *http.Response
		res, err = w.knowledge.send(ctx, "POST", path+"file", execution)
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(res.Body, model.MaxFileBytes+1))
			res.Body.Close()
		}
	}
	if err != nil {
		return nil, err
	}
	if len(data) > model.MaxFileBytes || len(data) == 0 || (manifest.MediaType == "application/pdf" && !bytes.HasPrefix(data, []byte("%PDF-"))) {
		return nil, fmt.Errorf("%w: expected PDF up to 20 MiB", processor.ErrInvalidFile)
	}
	hash := processor.Hash(data)
	if (manifest.SHA256 != "" && manifest.SHA256 != hash) || (manifest.SizeBytes != 0 && manifest.SizeBytes != int64(len(data))) {
		return nil, fmt.Errorf("%w: original file changed", processor.ErrInvalidFile)
	}
	directory, err := os.MkdirTemp("", "blix-ingestion-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	pdfPath := directory + "/original"
	if err := os.WriteFile(pdfPath, data, 0600); err != nil {
		return nil, err
	}
	text, err := p.Extract(ctx, pdfPath)
	if err != nil {
		return nil, err
	}
	if len(text) < 1 || len(text) > 500 {
		return nil, fmt.Errorf("%w: invalid page count", processor.ErrInvalidFile)
	}
	startData, _ := json.Marshal(execution)
	var start map[string]any
	if err := json.Unmarshal(startData, &start); err != nil {
		return nil, err
	}
	start["sha256"] = hash
	start["size_bytes"] = len(data)
	start["page_count"] = len(text)
	start["version"] = model.ExtractionVersion
	if err := w.knowledge.request(ctx, "POST", path+"start", start, nil); err != nil {
		return nil, err
	}
	completed := map[int]bool{}
	for _, number := range manifest.CompletedPages {
		completed[number] = true
	}
	err = w.processPages(ctx, len(text), completed, func(pageCtx context.Context, number int) error {
		page, err := p.Page(pageCtx, pdfPath, number, text[number-1])
		if err != nil {
			return err
		}
		page.Execution = execution
		if w.depot.Configured() {
			file, err := w.depot.UploadPageImage(pageCtx, fmt.Sprintf("%s-page-%d-v%d.jpg", input.DocumentID, number, model.ExtractionVersion), input.DocumentID, page.Image)
			if err != nil {
				return err
			}
			page.DepotFileID = file.ID
		} else {
			page.ImageBase64 = base64.StdEncoding.EncodeToString(page.Image)
		}
		return w.knowledge.request(pageCtx, "POST", path+fmt.Sprintf("pages/%d", number), page, nil)
	})
	if err != nil {
		return nil, err
	}
	if err := w.knowledge.request(ctx, "POST", path+"complete", execution, nil); err != nil {
		return nil, err
	}
	return map[string]any{"document_id": input.DocumentID, "generation": input.Generation, "page_count": len(text), "sha256": hash}, nil
}
func (w *Worker) processPages(ctx context.Context, count int, completed map[int]bool, process func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var tasks sync.WaitGroup
	var once sync.Once
	var firstError error
	for range min(w.config.PageConcurrency, count) {
		tasks.Go(func() {
			for number := range jobs {
				if ctx.Err() != nil {
					return
				}
				select {
				case w.pages <- struct{}{}:
				case <-ctx.Done():
					return
				}
				err := process(ctx, number)
				<-w.pages
				if err != nil {
					once.Do(func() { firstError = err; cancel() })
					return
				}
			}
		})
	}
schedule:
	for number := 1; number <= count; number++ {
		if completed[number] {
			continue
		}
		select {
		case jobs <- number:
		case <-ctx.Done():
			break schedule
		}
	}
	close(jobs)
	tasks.Wait()
	if firstError != nil {
		return firstError
	}
	return ctx.Err()
}
