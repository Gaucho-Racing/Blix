package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/gaucho-racing/blix/knowledge/api"
	"github.com/gaucho-racing/blix/knowledge/config"
	"github.com/gaucho-racing/blix/knowledge/database"
	"github.com/gaucho-racing/blix/knowledge/pkg/depot"
	"github.com/gaucho-racing/blix/knowledge/pkg/logger"
	"github.com/gaucho-racing/blix/knowledge/pkg/sentinel"
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
	sourceIndexDone := make(chan struct{})
	go func() {
		defer close(sourceIndexDone)
		sources.RunIndex(ctx, func(err error) { logger.SugarLogger.Warnf("source index: %v", err) })
	}()
	defer func() { stop(); <-sourceIndexDone }()
	pages := service.NewWikiService(db, wiki)
	index := service.NewWikiIndex(db, wiki)
	pages.SetIndex(index)
	indexDone := make(chan struct{})
	go func() {
		defer close(indexDone)
		index.Run(ctx, func(err error) { logger.SugarLogger.Warnf("wiki index: %v", err) })
	}()
	defer func() { stop(); <-indexDone }()
	sentinelClient := sentinel.NewClient(configuration.SentinelURL, configuration.SentinelClientID)
	if sentinelClient.Configured() {
		if err := sentinelClient.InitializeSigningKeys(ctx); err != nil {
			logger.SugarLogger.Warnf("initialize Sentinel signing keys: %v", err)
		}
	}
	auth := service.NewAuthService(sentinelClient)
	documents := service.NewDocumentService(db, configuration.DocumentStoragePath, service.PopplerExtractor{})
	documents.SetIntegration(depot.NewClient(configuration.DepotURL, configuration.DepotBucket, configuration.SentinelServiceAccountToken), service.NewDocumentRuntimeClient(configuration.RunwayURL, os.Getenv("BLIX_INTERNAL_TOKEN")))
	documentsDone := make(chan struct{})
	go func() {
		defer close(documentsDone)
		documents.Run(ctx, func(err error) { logger.SugarLogger.Warnf("document processing: %v", err) })
	}()
	defer func() { stop(); <-documentsDone }()
	return api.Run(ctx, configuration.Port, configuration.IsProduction(), sources, wiki, pages, documents, auth)
}
