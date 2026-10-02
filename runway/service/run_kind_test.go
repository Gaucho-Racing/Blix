package service

import (
	"fmt"
	"slices"
	"testing"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

func TestRunHistoryExcludesInternalDocumentRuns(t *testing.T) {
	f := setup(t)
	f.s.SetDocumentScopeValidator(&questionValidator{})
	if f.run.Run.Kind != model.RunKindChat {
		t.Fatalf("normal run has incorrect kind: %+v", f.run.Run)
	}
	documentID := identifier.New(identifier.Prefix("document"))
	ingestion, err := f.s.StartDocumentIngestion(f.ctx, DocumentIngestionRequest{
		Key: fmt.Sprintf("%s:1", documentID), Caller: f.owner, DocumentID: documentID,
		Generation: 1, Objective: "Read this document and draft wiki changes.",
	})
	if err != nil {
		t.Fatal(err)
	}
	question, err := f.s.CreateDocumentChat(f.ctx, documentID, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := f.s.CreateRun(f.ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Run{}).Where("id = ?", internal.Run.ID).Update("kind", model.RunKindInternal).Error; err != nil {
		t.Fatal(err)
	}
	other := model.CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: model.EntityTypeUser, UserID: identifier.New(identifier.User)}
	otherChat, err := f.s.CreateRun(f.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ingestion.RunID, question.Run.ID, internal.Run.ID} {
		if err := f.s.AuthorizeRunAccess(f.ctx, id, f.owner.EntityID, false); err != nil {
			t.Fatalf("internal audit access lost: %v", err)
		}
		details, err := f.s.GetRun(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if details["run"].(model.Run).Kind != model.RunKindInternal {
			t.Fatalf("internal kind was not persisted for %s", id)
		}
	}
	for _, status := range []string{"active", "completed", "cancelled"} {
		if err := f.db.Model(&model.Run{}).Where("id IN ?", []string{ingestion.RunID, question.Run.ID}).Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		all, err := f.s.ListAllRuns(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{f.run.Run.ID, otherChat.Run.ID, ingestion.RunID, question.Run.ID, internal.Run.ID} {
			if !slices.ContainsFunc(all, func(c model.Run) bool { return c.ID == id }) {
				t.Fatalf("admin history omitted %s with document status %s", id, status)
			}
		}
		for _, admin := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/admin=%t", status, admin), func(t *testing.T) {
				runs, err := f.s.ListRuns(f.ctx, f.owner.EntityID, admin, model.RunKindChat)
				if err != nil {
					t.Fatal(err)
				}
				ids := make([]string, 0, len(runs))
				for _, run := range runs {
					if run.Kind != model.RunKindChat {
						t.Fatalf("internal run leaked: %+v", run)
					}
					ids = append(ids, run.ID)
				}
				want := []string{f.run.Run.ID}
				if admin {
					want = append(want, otherChat.Run.ID)
				}
				slices.Sort(ids)
				slices.Sort(want)
				if !slices.Equal(ids, want) {
					t.Fatalf("history = %v; want %v", ids, want)
				}
			})
		}
	}
}

func TestRunKindRejectsInvalidValues(t *testing.T) {
	f := setup(t)
	if err := f.db.Model(&model.Run{}).Where("id = ?", f.run.Run.ID).Update("kind", "invalid").Error; err == nil {
		t.Fatal("invalid run kind was accepted")
	}
}

func TestRunListingSupportsKindFiltersAndOwnerIsolation(t *testing.T) {
	f := setup(t)
	internal, err := f.s.CreateRun(f.ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Run{}).Where("id = ?", internal.Run.ID).Update("kind", model.RunKindInternal).Error; err != nil {
		t.Fatal(err)
	}
	other := model.CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: model.EntityTypeUser, UserID: identifier.New(identifier.User)}
	foreign, err := f.s.CreateRun(f.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind  string
		admin bool
		want  []string
	}{
		{"", false, []string{f.run.Run.ID, internal.Run.ID}},
		{model.RunKindChat, false, []string{f.run.Run.ID}},
		{model.RunKindInternal, false, []string{internal.Run.ID}},
		{"", true, []string{f.run.Run.ID, internal.Run.ID, foreign.Run.ID}},
	} {
		runs, err := f.s.ListRuns(f.ctx, f.owner.EntityID, test.admin, test.kind)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(runs))
		for _, run := range runs {
			ids = append(ids, run.ID)
		}
		slices.Sort(ids)
		slices.Sort(test.want)
		if !slices.Equal(ids, test.want) {
			t.Fatalf("kind=%q admin=%t: runs=%v want=%v", test.kind, test.admin, ids, test.want)
		}
	}
	if _, err := f.s.ListRuns(f.ctx, f.owner.EntityID, false, "invalid"); err != ErrInvalid {
		t.Fatalf("invalid kind returned %v", err)
	}
}
