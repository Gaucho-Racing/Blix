package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
)

type ReviewChangesetInput struct {
	ReviewerEntityID string `json:"reviewer_entity_id"`
	Note             string `json:"note"`
}

func reviewUpdates(input ReviewChangesetInput, decision string) map[string]any {
	return map[string]any{"review_decision": decision, "reviewed_by": input.ReviewerEntityID,
		"review_note": strings.TrimSpace(input.Note), "reviewed_at": time.Now().UTC()}
}

func (s *WikiService) ReviewChangeset(ctx context.Context, id, decision string, input ReviewChangesetInput) (ChangesetDetails, error) {
	input.Note = strings.TrimSpace(input.Note)
	if !identifier.IsValid(id, identifier.Changeset) || !identifier.IsValid(input.ReviewerEntityID, identifier.Entity) ||
		len(input.Note) > 2000 || strings.ContainsRune(input.Note, 0) || (decision != "approve" && decision != "reject") ||
		(decision == "reject" && input.Note == "") {
		return ChangesetDetails{}, fmt.Errorf("%w: valid IDs and a review note up to 2000 bytes are required; rejection needs a reason", ErrInvalid)
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	if decision == "approve" {
		return s.applyReviewed(ctx, id, &input)
	}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result, err := s.lockChangeset(ctx, tx, id)
		if err != nil {
			return err
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
