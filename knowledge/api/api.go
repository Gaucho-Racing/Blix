package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gaucho-racing/blix/knowledge/pkg/logger"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	sources *service.SourceService
	wiki    *repository.Wiki
}

func Run(ctx context.Context, port string, production bool, sources *service.SourceService, wiki *repository.Wiki) error {
	router := InitializeRouter(production)
	InitializeRoutes(router, sources, wiki)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.SugarLogger.Infof("knowledge listening on :%s", port)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	}
}

func InitializeRouter(production bool) *gin.Engine {
	if production {
		gin.SetMode(gin.ReleaseMode)
	}
	return gin.Default()
}

func InitializeRoutes(router *gin.Engine, sources *service.SourceService, wiki *repository.Wiki) {
	handler := &Handler{sources: sources, wiki: wiki}
	router.GET("/ping", Ping)
	internal := router.Group("/internal/v1")
	internal.GET("/repository", handler.GetRepository)
	internal.POST("/sources", handler.CreateSource)
	internal.GET("/sources", handler.ListSources)
	internal.POST("/sources/:sourceID/records", handler.IngestSourceRecord)
	internal.GET("/source-records/:sourceRecordID", handler.GetSourceRecord)
}
