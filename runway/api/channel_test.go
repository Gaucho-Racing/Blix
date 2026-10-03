package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestChannelInputsRequireServiceAndCallerCredentials(t *testing.T) {
	t.Setenv("BLIX_INTERNAL_TOKEN", "service-secret")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	initializeDomainRoutes(router, &Handler{}, nil)
	for _, serviceToken := range []string{"", "wrong", "service-secret"} {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/channel-inputs", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+serviceToken)
		req.Header.Set("Content-Type", "application/json")
		if serviceToken != "service-secret" {
			req.Header.Set("X-Blix-Caller-Token", "claimed-user-token")
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("missing credential accepted: status=%d body=%s", res.Code, res.Body.String())
		}
	}
}
