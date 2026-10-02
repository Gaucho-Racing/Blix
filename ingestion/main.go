package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/gaucho-racing/blix/ingestion/api"
	"github.com/gaucho-racing/blix/ingestion/config"
	"github.com/gaucho-racing/blix/ingestion/pkg/logger"
	"github.com/gaucho-racing/blix/ingestion/processor"
	"github.com/gaucho-racing/blix/ingestion/service"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		panic(err)
	}
	logger.Init(configuration.IsProduction())
	defer logger.Logger.Sync()
	if err := run(configuration); err != nil {
		logger.SugarLogger.Fatalf("run ingestion: %v", err)
	}
}
func run(configuration config.Config) error {
	if err := processor.CheckDependencies(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	worker := service.NewWorker(configuration)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	defer func() { stop(); <-done }()
	logger.SugarLogger.Infof("ingestion listening on :%s", configuration.Port)
	return api.Run(ctx, configuration.Port, configuration.IsProduction(), worker)
}
