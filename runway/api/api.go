package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gaucho-racing/blix/runway/pkg/logger"
	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	executions *service.ExecutionService
}

func Run(ctx context.Context, port string, production bool, executions *service.ExecutionService) error {
	api := InitializeRouter(production)
	InitializeRoutes(api, executions)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.SugarLogger.Infof("runway listening on :%s", port)
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

func InitializeRoutes(router *gin.Engine, executions *service.ExecutionService) {
	handler := &Handler{executions: executions}
	router.GET("/ping", Ping)
	router.POST("/v1/runs", handler.CreateRun)
	router.POST("/v1/runs/:runID/messages", handler.AppendMessage)
	router.GET("/v1/runs/:runID/events", handler.ListEvents)
	router.POST("/internal/v1/executions/:executionID/claim", handler.ClaimExecution)
	router.POST("/internal/v1/executions/:executionID/model-checkpoints", handler.CheckpointModel)
	router.POST("/internal/v1/executions/:executionID/tool-checkpoints", handler.CheckpointTool)
	router.POST("/internal/v1/executions/:executionID/complete", handler.CompleteExecution)
	router.POST("/internal/v1/executions/:executionID/fail", handler.FailExecution)
}
