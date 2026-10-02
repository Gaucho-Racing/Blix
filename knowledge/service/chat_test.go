package service

import (
	"context"
	"errors"
	"github.com/gaucho-racing/blix/knowledge/config"
	"github.com/gaucho-racing/blix/knowledge/database"
	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

func TestRunCaptureIsAttributedAndIdempotent(t *testing.T) {
	port := os.Getenv("BLIX_TEST_DATABASE_PORT")
	if port == "" {
		t.Skip("BLIX_TEST_DATABASE_PORT is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, config.Config{DatabaseHost: "127.0.0.1", DatabasePort: port, DatabaseUser: "postgres", DatabasePassword: "blix-test", DatabaseName: "blix_test"})
	if err != nil {
		t.Fatal(err)
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	if err := db.Exec("TRUNCATE knowledge_source, knowledge_source_record, knowledge_source_identity RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	source := NewSourceService(db)
	input := IngestChatRecordInput{RunID: identifier.New(identifier.Run), SessionID: identifier.New(identifier.Session), MessageID: identifier.New(identifier.Message), EntityID: identifier.New(identifier.Entity), UserID: identifier.New(identifier.User), Content: "Original team knowledge", OccurredAt: time.Now().UTC()}
	captured, err := source.IngestChatRecord(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !captured.Created || captured.Record.CaptureRunID != input.RunID || captured.Record.CaptureSessionID != input.SessionID || captured.Record.Content != input.Content {
		t.Fatalf("capture=%+v", captured)
	}
	if captured.Record.EvidenceType != "human_statement" || captured.Record.CaptureMethod != "runtime_chat" {
		t.Fatal("capture lost evidence attribution")
	}
	repeated, err := source.IngestChatRecord(ctx, input)
	if err != nil || repeated.Created || repeated.Record.ID != captured.Record.ID {
		t.Fatalf("replay=%+v %v", repeated, err)
	}
	var count int64
	db.Model(&model.SourceRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("record count=%d", count)
	}
	input.Content = "Changed source text"
	if _, err := source.IngestChatRecord(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("modified evidence accepted: %v", err)
	}
}
