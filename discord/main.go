package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/gaucho-racing/blix/discord/api"
	"github.com/gaucho-racing/blix/discord/client"
	"github.com/gaucho-racing/blix/discord/config"
	"github.com/gaucho-racing/blix/discord/database"
	"github.com/gaucho-racing/blix/discord/service"
	"go.uber.org/zap"
)

func main() {
	c, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := zap.Must(zap.NewProduction())
	if !c.IsProduction() {
		log = zap.Must(zap.NewDevelopment())
	}
	defer log.Sync()
	if err = run(c, log.Sugar()); err != nil {
		log.Sugar().Fatalf("run Discord: %v", err)
	}
}
func run(c config.Config, log *zap.SugaredLogger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, c)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	gateway, err := client.NewDiscord(c.BotToken)
	if err != nil {
		return err
	}
	runner := &service.Runner{Gateway: gateway, Bridge: &service.Bridge{DB: db, Config: c, Identity: client.NewSentinel(c), Runtime: client.NewRunway(c), Discord: gateway, Log: log}}
	worker := make(chan error, 1)
	httpServer := make(chan error, 1)
	go func() { worker <- runner.Run(ctx) }()
	go func() { httpServer <- api.Run(ctx, c, runner) }()
	select {
	case err = <-worker:
		stop()
		<-httpServer
	case err = <-httpServer:
		stop()
		<-worker
	}
	return err
}
