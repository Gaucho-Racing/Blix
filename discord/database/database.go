package database

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/gaucho-racing/blix/discord/config"
	"github.com/gaucho-racing/blix/discord/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(ctx context.Context, c config.Config) (*gorm.DB, error) {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.DatabaseHost, c.DatabasePort), User: url.UserPassword(c.DatabaseUser, c.DatabasePassword), Path: "/" + c.DatabaseName}
	q := u.Query()
	q.Set("sslmode", c.DatabaseSSLMode)
	u.RawQuery = q.Encode()
	var db *gorm.DB
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		db, err = gorm.Open(postgres.Open(u.String()), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
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
		return nil, fmt.Errorf("connect Discord database: %w", err)
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('blix/discord/schema',0))").Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&model.Binding{}) && !tx.Migrator().HasColumn(&model.Binding{}, "id") {
			for _, statement := range []string{
				"ALTER TABLE discord_run_binding ADD COLUMN id text",
				"UPDATE discord_run_binding SET id = channel_id",
				"ALTER TABLE discord_run_binding ALTER COLUMN id SET NOT NULL",
				"ALTER TABLE discord_run_binding DROP CONSTRAINT discord_run_binding_pkey",
				"ALTER TABLE discord_run_binding ADD PRIMARY KEY (id)",
			} {
				if err := tx.Exec(statement).Error; err != nil {
					return err
				}
			}
		}
		return tx.AutoMigrate(&model.Inbox{}, &model.Binding{}, &model.Outbox{})
	})
	if err != nil {
		return nil, fmt.Errorf("create Discord schema: %w", err)
	}
	return db, nil
}
