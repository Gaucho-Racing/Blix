package api

import (
	"errors"
	"net/http"

	"github.com/gaucho-racing/blix/knowledge/pkg/logger"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func respondError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"
	switch {
	case errors.Is(err, service.ErrInvalid), errors.Is(err, repository.ErrInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrConflict), errors.Is(err, repository.ErrConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, repository.ErrUnavailable):
		status = http.StatusServiceUnavailable
		message = "wiki repository unavailable"
		logger.SugarLogger.Errorf("wiki unavailable: %v", err)
	default:
		logger.SugarLogger.Errorf("request failed: %v", err)
	}
	c.JSON(status, gin.H{"error": message})
}
