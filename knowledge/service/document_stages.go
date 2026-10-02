package service

import (
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var documentStageNames = [...]string{"uploading", "extracting", "indexing", "reading", "identifying_metadata", "reconciling"}

func initializeDocumentStages(doc *model.Document) {
	if len(doc.Stages) > 0 {
		return
	}
	doc.Stages = datatypes.NewJSONSlice(make([]model.DocumentStage, len(documentStageNames)))
	for i, name := range documentStageNames {
		doc.Stages[i] = model.DocumentStage{Name: name, Status: "waiting"}
	}
}

func documentStageComplete(doc model.Document, name string) bool {
	for _, stage := range doc.Stages {
		if stage.Name == name {
			return stage.Status == "completed"
		}
	}
	return false
}

func setDocumentStage(doc *model.Document, name, status string) {
	initializeDocumentStages(doc)
	now := time.Now().UTC()
	for i := range doc.Stages {
		stage := &doc.Stages[i]
		if stage.Name != name || stage.Status == "completed" {
			continue
		}
		if status == "running" && stage.Status != "running" {
			stage.StartedAt = &now
			stage.Generation = doc.Generation
		}
		stage.Status = status
		stage.UpdatedAt = now
		if status == "completed" {
			stage.CompletedAt = &now
		}
		return
	}
}

func saveDocumentProgress(tx *gorm.DB, doc *model.Document, updates map[string]any) error {
	updates["stages"] = doc.Stages
	return tx.Model(doc).Updates(updates).Error
}

func startDocumentStage(tx *gorm.DB, doc *model.Document, name string) error {
	if documentStageComplete(*doc, name) {
		return nil
	}
	setDocumentStage(doc, name, "running")
	return saveDocumentProgress(tx, doc, map[string]any{"status": name, "error": ""})
}

func stopDocumentStage(doc *model.Document, status string) {
	initializeDocumentStages(doc)
	stopped := false
	for _, stage := range doc.Stages {
		if stage.Status == "running" {
			setDocumentStage(doc, stage.Name, status)
			stopped = true
		}
	}
	if stopped {
		return
	}
	for _, stage := range doc.Stages {
		if stage.Status != "completed" {
			setDocumentStage(doc, stage.Name, status)
			return
		}
	}
}

func resetUnfinishedDocumentStages(doc *model.Document) {
	for i := range doc.Stages {
		stage := &doc.Stages[i]
		if stage.Status != "completed" {
			stage.Status = "waiting"
			stage.StartedAt = nil
			stage.CompletedAt = nil
			stage.UpdatedAt = time.Now().UTC()
		}
	}
}
