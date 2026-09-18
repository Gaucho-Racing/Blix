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
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=disable TimeZone=UTC",
		configuration.DatabaseHost,
		configuration.DatabaseUser,
		configuration.DatabasePassword,
		configuration.DatabaseName,
		configuration.DatabasePort,
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
	if err := db.AutoMigrate(
		&model.Source{},
		&model.SourceIdentity{},
		&model.SourceRecord{},
		&model.Episode{},
		&model.EpisodeRecord{},
		&model.EpisodeParticipant{},
		&model.Changeset{},
		&model.ChangesetFile{},
		&model.ChangesetSourceRecord{},
	); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}
