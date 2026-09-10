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
	auth          *service.AuthService
	executions    *service.ExecutionService
	workers       *service.WorkerService
	events        *service.EventHub
	workerUpdates *service.WorkerHub
}

func Run(
	ctx context.Context,
	port string,
	production bool,
	auth *service.AuthService,
	executions *service.ExecutionService,
	workers *service.WorkerService,
	events *service.EventHub,
	workerUpdates *service.WorkerHub,
) error {
	api := InitializeRouter(production)
	InitializeRoutes(api, auth, executions, workers, events, workerUpdates)
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

func InitializeRoutes(
	router *gin.Engine,
	auth *service.AuthService,
	executions *service.ExecutionService,
	workers *service.WorkerService,
	events *service.EventHub,
	workerUpdates *service.WorkerHub,
) {
	handler := &Handler{auth: auth, executions: executions, workers: workers, events: events, workerUpdates: workerUpdates}
	router.GET("/ping", Ping)
	router.GET("/v1/auth/config", handler.GetAuthConfiguration)
	router.POST("/v1/auth/login", handler.LoginWithSentinel)
	router.GET("/v1/auth/session", RequireAuthentication(auth), handler.GetSession)
	router.POST("/v1/auth/refresh", handler.RefreshSession)
	router.POST("/v1/auth/logout", handler.Logout)
	router.GET("/v1/events/stream", handler.StreamAllEvents)
	router.GET("/v1/workers", handler.ListWorkers)
	router.GET("/v1/workers/stream", handler.StreamWorkers)
	router.GET("/v1/runs", handler.ListRuns)
	router.POST("/v1/runs", RequireAuthentication(auth), handler.CreateRun)
	router.GET("/v1/runs/:runID", handler.GetRun)
	router.POST("/v1/runs/:runID/messages", RequireAuthentication(auth), handler.AppendMessage)
	router.GET("/v1/runs/:runID/events", handler.ListEvents)
	router.GET("/v1/runs/:runID/events/stream", handler.StreamRunEvents)
	router.GET("/v1/executions/:executionID", handler.GetExecution)
	router.POST("/internal/v1/workers/register", handler.RegisterWorker)
	router.POST("/internal/v1/workers/:workerID/heartbeat", handler.HeartbeatWorker)
	router.POST("/internal/v1/executions/:executionID/claim", handler.ClaimExecution)
	router.POST("/internal/v1/executions/:executionID/heartbeat", handler.HeartbeatExecution)
	router.POST("/internal/v1/executions/:executionID/delegations", handler.DelegateExecution)
	router.GET("/internal/v1/executions/:executionID/result", handler.GetExecutionResult)
	router.POST("/internal/v1/executions/:executionID/model-checkpoints", handler.CheckpointModel)
	router.POST("/internal/v1/executions/:executionID/tool-checkpoints", handler.CheckpointTool)
	router.POST("/internal/v1/executions/:executionID/complete", handler.CompleteExecution)
	router.POST("/internal/v1/executions/:executionID/agent-complete", handler.CompleteAgentExecution)
	router.POST("/internal/v1/executions/:executionID/fail", handler.FailExecution)
}
