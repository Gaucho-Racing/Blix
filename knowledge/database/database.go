package database

import (
	"context"
	"fmt"
	"time"

	"github.com/gaucho-racing/blix/knowledge/config"
	"github.com/gaucho-racing/blix/knowledge/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Open(ctx context.Context, configuration config.Config) (*gorm.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=UTC",
		configuration.DatabaseHost,
		configuration.DatabaseUser,
		configuration.DatabasePassword,
		configuration.DatabaseName,
		configuration.DatabasePort,
		configuration.DatabaseSSLMode,
	)
	var db *gorm.DB
	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return nil, fmt.Errorf("enable pgvector: %w", err)
	}
	if err := db.AutoMigrate(
		&model.Embedding{},
		&model.WikiIndexState{},
		&model.WikiPage{},
		&model.WikiSection{},
		&model.Document{},
		&model.DocumentPage{},
		&model.DocumentPageImage{},
		&model.DocumentChunk{}, &model.DocumentNotesRevision{},
		&model.Source{},
		&model.SourceIdentity{},
		&model.SourceRecord{},
		&model.SourceChunk{}, &model.SourceRecordIndex{},
		&model.Episode{},
		&model.EpisodeRecord{},
		&model.EpisodeParticipant{},
		&model.Changeset{},
		&model.ChangesetFile{},
		&model.ChangesetSourceRecord{},
		&model.ChangesetDocument{},
	); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_knowledge_wiki_search ON knowledge_wiki_section USING GIN (search_vector)`).Error; err != nil {
		return nil, fmt.Errorf("create wiki search index: %w", err)
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS idx_source_chunk_search ON knowledge_source_chunk USING GIN (search_vector)",
		"CREATE INDEX IF NOT EXISTS idx_source_chunk_rules ON knowledge_source_chunk USING GIN (rule_ids)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			return nil, fmt.Errorf("create source search index: %w", err)
		}
	}
	return db, nil
}
