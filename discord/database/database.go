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
	if err = db.AutoMigrate(&model.Inbox{}, &model.Binding{}, &model.Outbox{}); err != nil {
		return nil, fmt.Errorf("create Discord schema: %w", err)
	}
	return db, nil
}
