package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/gaucho-racing/blix/knowledge/api"
	"github.com/gaucho-racing/blix/knowledge/config"
	"github.com/gaucho-racing/blix/knowledge/database"
	"github.com/gaucho-racing/blix/knowledge/pkg/logger"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"github.com/gaucho-racing/blix/knowledge/service"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		panic(err)
	}
	logger.Init(configuration.IsProduction())
	defer logger.Logger.Sync()
	if err := run(configuration); err != nil {
		logger.SugarLogger.Fatalf("run knowledge: %v", err)
	}
}

func run(configuration config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, configuration)
	if err != nil {
		return err
	}
	wiki, err := repository.OpenWiki(ctx, configuration)
	if err != nil {
		logger.SugarLogger.Warnf("initialize wiki repository: %v", err)
	}
	sources := service.NewSourceService(db)
	return api.Run(ctx, configuration.Port, configuration.IsProduction(), sources, wiki)
}
