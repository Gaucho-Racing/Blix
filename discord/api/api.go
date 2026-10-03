package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gaucho-racing/blix/discord/config"
	"github.com/gaucho-racing/blix/discord/service"
	"github.com/gin-gonic/gin"
)

func Run(ctx context.Context, c config.Config, runner *service.Runner) error {
	if c.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/ping", func(g *gin.Context) { g.JSON(http.StatusOK, gin.H{"service": "discord"}) })
	router.GET("/ready", func(g *gin.Context) {
		ready := runner != nil && runner.Ready.Load() && runner.Gateway.Connected.Load()
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		g.JSON(status, gin.H{"ready": ready})
	})
	server := &http.Server{Addr: ":" + c.Port, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(stop)
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
