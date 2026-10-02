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
	sources   *service.SourceService
	wiki      *repository.Wiki
	pages     *service.WikiService
	documents *service.DocumentService
}

func Run(ctx context.Context, port string, production bool, sources *service.SourceService, wiki *repository.Wiki, pages *service.WikiService, documents *service.DocumentService, auth *service.AuthService) error {
	router := InitializeRouter(production)
	InitializeRoutes(router, sources, wiki, pages, documents, auth)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       90 * time.Second,
		WriteTimeout:      120 * time.Second,
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

func InitializeRoutes(router *gin.Engine, sources *service.SourceService, wiki *repository.Wiki, pages *service.WikiService, documents *service.DocumentService, auth *service.AuthService) {
	handler := &Handler{sources: sources, wiki: wiki, pages: pages, documents: documents}
	uploads := router.Group("/v1/documents", RequireAuthentication(auth), RequireGroup("BlixUsers", "BlixAdmins"))
	uploads.GET("", handler.ListDocuments)
	uploads.POST("/uploads", handler.InitiateDocumentUpload)
	uploads.POST("/:documentID/review/:decision", handler.ReviewDocument)
	uploads.POST("/:documentID/complete", handler.CompleteDocumentUpload)
	uploads.POST("/:documentID/ingestion/:operation", handler.ControlDocument)
	uploads.GET("/:documentID/chunks/:number", handler.ReadDocumentChunk)
	uploads.GET("/:documentID", handler.GetDocument)
	uploads.PATCH("/:documentID/metadata", handler.UpdateDocumentMetadata)
	uploads.GET("/:documentID/pages/:number", handler.GetDocumentPage)
	uploads.GET("/:documentID/pages/:number/image", handler.GetDocumentPageImage)
	uploads.GET("/:documentID/file", handler.GetDocumentFile)
	reads := router.Group("/v1/wiki", RequireAuthentication(auth), RequireGroup("BlixUsers", "BlixAdmins"))
	reads.GET("/search", handler.SearchWiki)
	reads.GET("/page", handler.ReadWikiPage)
	reads.GET("/changesets/:changesetID", handler.GetWikiChangeset)
	router.GET("/ping", Ping)
	reviews := router.Group("/v1/changesets", RequireAuthentication(auth), RequireGroup("BlixUsers", "BlixAdmins"))
	reviews.GET("", RequireGroup("BlixAdmins"), handler.ListWikiChangesets)
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
	internal.GET("/documents/:documentID", handler.GetAgentDocument)
	internal.GET("/documents/:documentID/pages/:number/image", handler.GetAgentPageImage)
	internal.POST("/documents/access", handler.ValidateDocumentAccess)
	internal.GET("/document-evidence", handler.RetrieveDocumentEvidence)
	internal.POST("/documents/:documentID/metadata", handler.SaveExtractedDocumentMetadata)
	internal.GET("/documents/:documentID/chunks/:number", handler.ReadAgentDocumentChunk)
	internal.GET("/documents/:documentID/pages/:number", handler.ReadAgentDocumentPage)
	internal.POST("/documents/:documentID/chunks/:number", handler.SaveDocumentChunk)
	internal.POST("/documents/:documentID/pages/:number/visual-analysis", handler.SaveDocumentVisualAnalysis)
	internal.POST("/documents/:documentID/ingestion/:operation", handler.ProcessDocument)
	internal.POST("/documents/:documentID/extraction/pages/:number", handler.SaveExtractionPage)
	internal.POST("/documents/:documentID/extraction/:operation", handler.ExtractDocument)
	internal.GET("/repository", handler.GetRepository)
	internal.POST("/repository/seed", handler.SeedWiki)
	internal.GET("/pages", handler.ListWikiPages)
	internal.GET("/page", handler.ReadWikiPage)
	internal.GET("/search", handler.SearchWiki)
	internal.POST("/search", handler.SearchWikiBatch)
	internal.POST("/pages/read", handler.ReadWikiPageBatch)
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
