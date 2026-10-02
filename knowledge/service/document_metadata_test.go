package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/datatypes"
)

func TestDocumentTagsNormalizeAndRejectInvalidLabels(t *testing.T) {
	tags, err := normalizeDocumentTags([]string{" Formula   SAE ", "braking", "FORMULA SAE"})
	if err != nil || !slices.Equal(tags, []string{"formula sae", "braking"}) {
		t.Fatalf("normalized tags: %v %v", tags, err)
	}
	for _, input := range [][]string{
		{""}, {"   "}, {"two\nlines"}, {"nul\x00"}, {string([]byte{0xff})},
		{strings.Repeat("x", 65)}, {strings.Repeat("é", 33)},
		{"1", "2", "3", "4", "5", "6", "7", "8", "9"},
	} {
		if _, err := normalizeDocumentTags(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid tags accepted: %q %v", input, err)
		}
	}
	for _, input := range [][]string{nil, {}, {strings.Repeat("é", 32)}} {
		if tags, err := normalizeDocumentTags(input); err != nil || tags == nil {
			t.Fatalf("valid tags rejected or returned null: %v %v", tags, err)
		}
	}
}

func TestDocumentTagsPersistAndCanBeClearedWithRevisionChecks(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	doc := model.Document{ID: identifier.New(identifier.Document), UploadedBy: identifier.New(identifier.Entity), Tags: datatypes.NewJSONSlice([]string{}), Status: "imported", Title: "Document"}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	service := NewDocumentService(db, t.TempDir(), nil)
	tags := []string{" Driverless ", "Autonomy", "driverless"}
	patch := DocumentMetadataPatch{Revision: 0, Tags: &tags}
	if _, err := service.UpdateMetadata(context.Background(), doc.ID, identifier.New(identifier.Entity), patch); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign tag editor accepted: %v", err)
	}
	saved, err := service.UpdateMetadata(context.Background(), doc.ID, doc.UploadedBy, patch)
	if err != nil || !slices.Equal(saved.Document.Tags, []string{"driverless", "autonomy"}) || saved.Document.Title != doc.Title {
		t.Fatalf("tags did not persist independently: %+v %v", saved.Document, err)
	}
	if _, err := service.UpdateMetadata(context.Background(), doc.ID, doc.UploadedBy, patch); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale tag revision accepted: %v", err)
	}
	empty := []string{}
	cleared, err := service.UpdateMetadata(context.Background(), doc.ID, doc.UploadedBy, DocumentMetadataPatch{Revision: 1, Tags: &empty})
	if err != nil || cleared.Document.Tags == nil || len(cleared.Document.Tags) != 0 {
		t.Fatalf("tags were not cleared to an empty array: %+v %v", cleared.Document, err)
	}
	var overrides map[string]bool
	if err := json.Unmarshal(cleared.Document.MetadataOverrides, &overrides); err != nil {
		t.Fatal(err)
	}
	if !overrides["tags"] {
		t.Fatal("tag override was not recorded")
	}
}
