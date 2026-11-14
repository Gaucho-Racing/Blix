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

const adminGroup = "BlixAdmins"

type Handler struct {
	auth          *service.AuthService
	identities    *service.IdentityService
	executions    *service.ExecutionService
	workers       *service.WorkerService
	usage         *service.UsageService
	events        *service.EventHub
	workerUpdates *service.WorkerHub
}

func Run(
	ctx context.Context,
	port string,
	production bool,
	auth *service.AuthService,
	identities *service.IdentityService,
	executions *service.ExecutionService,
	workers *service.WorkerService,
	usage *service.UsageService,
	events *service.EventHub,
	workerUpdates *service.WorkerHub,
) error {
	api := InitializeRouter(production)
	InitializeRoutes(api, auth, identities, executions, workers, usage, events, workerUpdates)
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
	identities *service.IdentityService,
	executions *service.ExecutionService,
	workers *service.WorkerService,
	usage *service.UsageService,
	events *service.EventHub,
	workerUpdates *service.WorkerHub,
) {
	handler := &Handler{
		auth:          auth,
		identities:    identities,
		executions:    executions,
		workers:       workers,
		usage:         usage,
		events:        events,
		workerUpdates: workerUpdates,
	}
	router.GET("/ping", Ping)
	router.GET("/v1/auth/config", handler.GetAuthConfiguration)
	router.POST("/v1/auth/login", handler.LoginWithSentinel)
	router.GET("/v1/auth/session", RequireAuthentication(auth), handler.GetSession)
	router.GET("/auth/users/@me", RequireAuthentication(auth), handler.GetCurrentUser)
	router.POST("/identities/resolve", RequireAuthentication(auth), RequireGroup("BlixUsers", adminGroup), handler.ResolveIdentities)
	router.POST("/v1/auth/refresh", handler.RefreshSession)
	router.POST("/v1/auth/logout", handler.Logout)
	router.GET("/v1/events/stream", RequireStreamAuthentication(auth), RequireGroup(adminGroup), handler.StreamAllEvents)
	router.GET("/v1/workers", RequireAuthentication(auth), RequireGroup(adminGroup), handler.ListWorkers)
	router.GET("/v1/workers/stream", RequireStreamAuthentication(auth), RequireGroup(adminGroup), handler.StreamWorkers)
	router.GET("/v1/admin/costs", RequireAuthentication(auth), RequireGroup(adminGroup), handler.GetCostSummary)
	router.GET("/v1/admin/runs/:runID/costs", RequireAuthentication(auth), RequireGroup(adminGroup), handler.GetRunCosts)
	initializeDomainRoutes(router, handler, auth)
}
