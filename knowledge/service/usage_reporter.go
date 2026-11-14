package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

const (
	usageReportQueueSize = 512
	usageReportAttempts  = 3
)

type usageReport struct {
	SourceKey   string `json:"source_key"`
	Category    string `json:"category"`
	Resource    string `json:"resource"`
	ProfileID   string `json:"profile_id"`
	InputTokens int    `json:"input_tokens"`
}

type UsageReporter struct {
	runtime *DocumentRuntimeClient
	queue   chan usageReport
	report  func(error)
}

func NewUsageReporter(runtime *DocumentRuntimeClient, report func(error)) *UsageReporter {
	return &UsageReporter{runtime: runtime, queue: make(chan usageReport, usageReportQueueSize), report: report}
}

func (r *UsageReporter) RecordEmbedding(model string, inputTokens int) {
	if inputTokens <= 0 {
		return
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		r.report(err)
		return
	}
	select {
	case r.queue <- usageReport{SourceKey: "knowledge-embedding:" + hex.EncodeToString(key), Category: "embedding", Resource: model, ProfileID: "knowledge_embeddings", InputTokens: inputTokens}:
	default:
		r.report(errUsageQueueFull)
	}
}

func (r *UsageReporter) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.drain()
			return
		case item := <-r.queue:
			r.send(ctx, item)
		}
	}
}

func (r *UsageReporter) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case item := <-r.queue:
			r.send(ctx, item)
		default:
			return
		}
	}
}

func (r *UsageReporter) send(ctx context.Context, item usageReport) {
	var err error
	for attempt := range usageReportAttempts {
		if err = r.runtime.request(ctx, "POST", "usage", item, nil); err == nil || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	if err != nil {
		r.report(err)
	}
}
