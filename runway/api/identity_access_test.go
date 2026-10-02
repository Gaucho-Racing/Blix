package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

func TestIdentityLookupAllowsTeamMembersWithoutOpeningAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                        string
		groups                      []string
		authenticated               bool
		identityStatus, adminStatus int
	}{
		{"anonymous", nil, false, http.StatusUnauthorized, http.StatusUnauthorized},
		{"nonmember", []string{"OtherGroup"}, true, http.StatusForbidden, http.StatusForbidden},
		{"member", []string{"BlixUsers"}, true, http.StatusOK, http.StatusForbidden},
		{"admin", []string{adminGroup}, true, http.StatusOK, http.StatusOK},
		{"member and admin", []string{"BlixUsers", adminGroup}, true, http.StatusOK, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			if tc.authenticated {
				router.Use(func(c *gin.Context) {
					c.Set(callerContextKey, service.CallerContext{EntityID: identifier.New(identifier.Entity), Groups: tc.groups})
					c.Next()
				})
			}
			ok := func(c *gin.Context) { c.Status(http.StatusOK) }
			router.POST("/identities/resolve", RequireGroup("BlixUsers", adminGroup), ok)
			router.GET("/admin-only", RequireGroup(adminGroup), ok)
			for _, route := range []struct {
				method, path string
				status       int
			}{
				{http.MethodPost, "/identities/resolve", tc.identityStatus},
				{http.MethodGet, "/admin-only", tc.adminStatus},
			} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
				if response.Code != route.status {
					t.Fatalf("%s status = %d, want %d", route.path, response.Code, route.status)
				}
			}
		})
	}
}
