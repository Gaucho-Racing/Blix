package main

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/gaucho-racing/blix/runway/api"
	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/database"
	"github.com/gaucho-racing/blix/runway/mqtt"
	"github.com/gaucho-racing/blix/runway/pkg/logger"
	"github.com/gaucho-racing/blix/runway/pkg/sentinel"
	"github.com/gaucho-racing/blix/runway/service"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		panic(err)
	}
	logger.Init(configuration.IsProduction())
	defer logger.Logger.Sync()
	if err := run(configuration); err != nil {
		logger.SugarLogger.Fatalf("run runway: %v", err)
	}
}

func run(configuration config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, configuration)
	if err != nil {
		return err
	}
	eventHub := service.NewEventHub()
	workerHub := service.NewWorkerHub()
	publisher, err := mqtt.Connect(ctx, configuration, eventHub.Publish)
	if err != nil {
		return err
	}
	defer func() {
		disconnectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := publisher.Close(disconnectCtx); err != nil {
			logger.SugarLogger.Errorf("disconnect MQTT publisher: %v", err)
		}
	}()

	workers := service.NewWorkerService(db, workerHub, configuration)
	sentinelClient := sentinel.NewClient(
		configuration.SentinelURL,
		configuration.SentinelClientID,
		configuration.SentinelClientSecret,
	)
	if sentinelClient.Configured() {
		if err := sentinelClient.InitializeSigningKeys(ctx); err != nil {
			logger.SugarLogger.Warnf("initialize Sentinel signing keys: %v", err)
		}
	}
	auth := service.NewAuthService(sentinelClient, configuration.SentinelScopes)
	identities := service.NewIdentityService(sentinelClient, configuration.SentinelServiceToken)
	executions := service.NewExecutionService(db, workers, configuration)
	outbox := service.NewOutboxDispatcher(db, publisher, configuration.OutboxPollInterval)
	leaseReaper := service.NewExecutionLeaseReaper(executions, configuration.LeaseSweepInterval)
	go outbox.Run(ctx)
	go leaseReaper.Run(ctx)

	return api.Run(
		ctx,
		configuration.Port,
		configuration.IsProduction(),
		auth,
		identities,
		executions,
		workers,
		eventHub,
		workerHub,
		service.NewKnowledgeService(configuration.KnowledgeURL),
	)
}
