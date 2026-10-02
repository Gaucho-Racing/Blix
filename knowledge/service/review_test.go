package service

import (
	"context"
	"errors"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

func TestChangesetReviewPermissionsAndAudit(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	id := publishFixture(t, db)
	repo := &publishingRepository{}
	s := NewWikiService(db, repo)
	user := CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: "USER", Groups: []string{"BlixUsers"}}
	admin := CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: "USER", Groups: []string{"BlixAdmins"}}
	ctx := context.Background()
	for _, input := range []struct {
		caller   CallerContext
		decision string
	}{
		{user, "approve"},
		{CallerContext{EntityID: user.EntityID, EntityType: "USER"}, "reject"},
		{CallerContext{EntityID: user.EntityID, EntityType: "SERVICE_ACCOUNT", Groups: []string{"BlixAdmins"}}, "approve"},
		{CallerContext{EntityID: user.EntityID, EntityType: "SERVICE_ACCOUNT", Groups: []string{"BlixUsers"}}, "reject"},
	} {
		if _, err := s.ReviewChangeset(ctx, id, input.decision, ReviewChangesetInput{Caller: input.caller, Note: "Review"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unauthorized review accepted: %+v %v", input, err)
		}
	}
	if _, err := s.ReviewChangeset(ctx, id, "reject", ReviewChangesetInput{Caller: user, Note: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejection without a reason accepted: %v", err)
	}
	rejected, err := s.ReviewChangeset(ctx, id, "reject", ReviewChangesetInput{Caller: user, Note: "  Rule exception is missing.  "})
	if err != nil || rejected.Changeset.Status != model.ChangesetStatusRejected || rejected.Changeset.ReviewedBy != user.EntityID || rejected.Changeset.ReviewNote != "Rule exception is missing." || rejected.Changeset.ReviewedAt == nil {
		t.Fatalf("rejection did not record the signed-in reviewer: %+v %v", rejected.Changeset, err)
	}
	replay, err := s.ReviewChangeset(ctx, id, "reject", ReviewChangesetInput{Caller: admin, Note: "Different reason"})
	if err != nil || replay.Changeset.ReviewedBy != user.EntityID || replay.Changeset.ReviewNote != rejected.Changeset.ReviewNote {
		t.Fatalf("rejection replay changed the original audit: %+v %v", replay.Changeset, err)
	}
	if _, err := s.ReviewChangeset(ctx, id, "approve", ReviewChangesetInput{Caller: admin}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected changeset was approved: %v", err)
	}
	if repo.prepared != 0 || repo.published != 0 {
		t.Fatal("rejection or forbidden approval published content")
	}
	id = publishFixture(t, db)
	approved, err := s.ReviewChangeset(ctx, id, "approve", ReviewChangesetInput{Caller: admin, Note: "Verified against the source."})
	if err != nil || approved.Changeset.Status != model.ChangesetStatusCommitted || approved.Changeset.ReviewedBy != admin.EntityID || approved.Changeset.ReviewDecision != "approved" || repo.published != 1 {
		t.Fatalf("admin approval failed: %+v %v", approved.Changeset, err)
	}
	if _, err := s.ReviewChangeset(ctx, id, "reject", ReviewChangesetInput{Caller: user, Note: "Too late"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("published changeset was rejected: %v", err)
	}
}
