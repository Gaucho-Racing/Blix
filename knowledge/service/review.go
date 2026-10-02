package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
)

type ReviewInput struct {
	Caller CallerContext `json:"-"`
	Note   string        `json:"note"`
}

func reviewUpdates(input ReviewInput, decision string) map[string]any {
	return map[string]any{"review_decision": decision, "reviewed_by": input.Caller.EntityID,
		"review_note": strings.TrimSpace(input.Note), "reviewed_at": time.Now().UTC()}
}

func (s *WikiService) ReviewChangeset(ctx context.Context, id, decision string, input ReviewInput) (ChangesetDetails, error) {
	if err := validateReviewInput(input, decision); err != nil {
		return ChangesetDetails{}, err
	}
	if !identifier.IsValid(id, identifier.Changeset) {
		return ChangesetDetails{}, ErrInvalid
	}
	input.Note = strings.TrimSpace(input.Note)

	s.writer.Lock()
	defer s.writer.Unlock()
	if decision == "approve" {
		return s.applyReviewed(ctx, id, &input)
	}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockChangesetDocuments(tx, id); err != nil {
			return err
		}
		result, err := s.lockChangeset(ctx, tx, id)
		if err != nil {
			return err
		}
		if result.Changeset.PublicationLeaseUntil != nil && result.Changeset.PublicationLeaseUntil.After(time.Now()) {
			return fmt.Errorf("%w: publication is already running", ErrConflict)
		}
		if result.Changeset.Status == model.ChangesetStatusRejected {
			return nil
		}
		if result.Changeset.ResultCommitSHA != "" || (result.Changeset.Status != model.ChangesetStatusProposed && result.Changeset.Status != model.ChangesetStatusFailed) {
			return fmt.Errorf("%w: publication has started; retry publication to reconcile its outcome before making further changes", ErrConflict)
		}
		updates := reviewUpdates(input, "rejected")
		updates["status"] = model.ChangesetStatusRejected
		return tx.Model(&model.Changeset{}).Where("id = ?", id).Updates(updates).Error
	})
	if err != nil {
		return ChangesetDetails{}, err
	}
	return s.GetChangeset(ctx, id)
}

func validateReviewInput(input ReviewInput, decision string) error {
	isAdmin := slices.Contains(input.Caller.Groups, "BlixAdmins")
	canReview := input.Caller.EntityType == "USER" && (isAdmin || slices.Contains(input.Caller.Groups, "BlixUsers"))
	if !canReview || (decision == "approve" && !isAdmin) {
		return ErrForbidden
	}
	note := strings.TrimSpace(input.Note)
	if !identifier.IsValid(input.Caller.EntityID, identifier.Entity) || len(note) > 2000 || !utf8.ValidString(note) || strings.ContainsRune(note, 0) || (decision != "approve" && decision != "reject") || (decision == "reject" && note == "") {
		return fmt.Errorf("%w: a review note up to 2000 UTF-8 bytes is required; rejection needs a reason", ErrInvalid)
	}
	return nil
}
