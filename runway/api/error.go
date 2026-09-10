package api

import (
	"errors"
	"net/http"

	"github.com/gaucho-racing/blix/runway/pkg/logger"
	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func respondError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"
	switch {
	case errors.Is(err, service.ErrInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrUnauthorized):
		status = http.StatusUnauthorized
		message = err.Error()
	case errors.Is(err, service.ErrForbidden):
		status = http.StatusForbidden
		message = err.Error()
	case errors.Is(err, service.ErrUnavailable):
		status = http.StatusServiceUnavailable
		message = err.Error()
	default:
		logger.SugarLogger.Errorf("request failed: %v", err)
	}
	c.JSON(status, gin.H{"error": message})
}
