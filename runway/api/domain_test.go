package api

import (
	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalRoutesRequireServiceCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	initializeDomainRoutes(router, &Handler{executions: service.NewExecutionService(nil, nil, config.Config{})}, nil)
	for _, test := range []struct {
		configured, provided string
		status               int
	}{
		{"", "", http.StatusUnauthorized},
		{"secret", "wrong", http.StatusUnauthorized},
		{"secret", "secret", http.StatusBadRequest},
	} {
		t.Setenv("BLIX_INTERNAL_TOKEN", test.configured)
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/attempts/invalid/claim", strings.NewReader(`{"worker_id":"invalid","runtime_kind":"api"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+test.provided)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
