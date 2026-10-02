package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/knowledge/pkg/logger"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	sources *service.SourceService
	wiki    *repository.Wiki
	pages   *service.WikiService
}

func Run(ctx context.Context, port string, production bool, sources *service.SourceService, wiki *repository.Wiki, pages *service.WikiService, auth *service.AuthService) error {
	router := InitializeRouter(production)
	InitializeRoutes(router, sources, wiki, pages, auth)
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

func InitializeRoutes(router *gin.Engine, sources *service.SourceService, wiki *repository.Wiki, pages *service.WikiService, auth *service.AuthService) {
	handler := &Handler{sources: sources, wiki: wiki, pages: pages}
	router.GET("/ping", Ping)
	reviews := router.Group("/v1/changesets", RequireAuthentication(auth), RequireGroup("BlixAdmins"))
	reviews.GET("", handler.ListWikiChangesets)
	reviews.GET("/:changesetID", handler.GetWikiChangeset)
	reviews.GET("/:changesetID/base", handler.GetWikiChangesetBase)
	reviews.POST("/:changesetID/review/:decision", handler.ReviewWikiChangeset)
	internal := router.Group("/internal/v1", func(c *gin.Context) {
		token := os.Getenv("BLIX_INTERNAL_TOKEN")
		provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(provided)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid service credentials"})
			return
		}
		c.Next()
	})
	internal.GET("/repository", handler.GetRepository)
	internal.POST("/repository/seed", handler.SeedWiki)
	internal.GET("/pages", handler.ListWikiPages)
	internal.GET("/page", handler.ReadWikiPage)
	internal.GET("/search", handler.SearchWiki)
	internal.POST("/changesets", handler.CreateWikiChangeset)
	internal.GET("/changesets", handler.ListWikiChangesets)
	internal.GET("/changesets/search", handler.SearchPendingWikiChangesets)
	internal.GET("/changesets/:changesetID", handler.GetWikiChangeset)
	internal.GET("/changesets/:changesetID/base", handler.GetWikiChangesetBase)
	internal.POST("/changesets/:changesetID/apply", handler.ApplyWikiChangeset)
	internal.POST("/sources", handler.CreateSource)
	internal.GET("/sources", handler.ListSources)
	internal.GET("/sources/:sourceID", handler.GetSource)
	internal.GET("/sources/:sourceID/records", handler.ListSourceRecords)
	internal.POST("/source-records", handler.CaptureSourceRecord)
	internal.POST("/chat-records", handler.IngestChatRecord)
	internal.POST("/sources/:sourceID/records", handler.IngestSourceRecord)
	internal.GET("/source-records/:sourceRecordID", handler.GetSourceRecord)
}
