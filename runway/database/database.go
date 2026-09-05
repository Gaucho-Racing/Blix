package database

import (
	"context"
	"fmt"
	"time"

	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/model"
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
	var database *gorm.DB
	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		database, err = gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true})
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
	if err := database.AutoMigrate(
		&model.Run{},
		&model.AgentExecution{},
		&model.ExecutionEvent{},
		&model.Message{},
		&model.ToolCall{},
		&model.OutboxMessage{},
	); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return database, nil
}
