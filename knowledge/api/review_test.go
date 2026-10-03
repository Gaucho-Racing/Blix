package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"github.com/gaucho-racing/blix/knowledge/pkg/sentinel"
	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestChangesetRoutesEnforceReviewRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/core/keys" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "review-test",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
		}}})
	}))
	defer keys.Close()
	router := gin.New()
	InitializeRoutes(router, nil, nil, service.NewWikiService(nil, nil), service.NewDocumentService(nil, "", nil), service.NewAuthService(sentinel.NewClient(keys.URL, "blix-test")))
	for _, tc := range []struct {
		name, method, path, entityType string
		groups                         []string
		status                         int
	}{
		{"anonymous document indexes", "GET", "/v1/documents/invalid/indexes", "", nil, 401},
		{"nonmember document indexes", "GET", "/v1/documents/invalid/indexes", "USER", nil, 403},
		{"user document indexes", "GET", "/v1/documents/invalid/indexes", "USER", []string{"BlixUsers"}, 400},
		{"invalid index limit", "GET", "/v1/documents/invalid/indexes?limit=nope", "USER", []string{"BlixUsers"}, 400},
		{"anonymous document rejection", "POST", "/v1/documents/invalid/review/reject", "", nil, 401},
		{"nonmember document rejection", "POST", "/v1/documents/invalid/review/reject", "USER", nil, 403},
		{"user document rejection", "POST", "/v1/documents/invalid/review/reject", "USER", []string{"BlixUsers"}, 400},
		{"user document approval", "POST", "/v1/documents/invalid/review/approve", "USER", []string{"BlixUsers"}, 403},
		{"admin document approval", "POST", "/v1/documents/invalid/review/approve", "USER", []string{"BlixAdmins"}, 400},
		{"admin document rejection", "POST", "/v1/documents/invalid/review/reject", "USER", []string{"BlixAdmins"}, 400},
		{"service account document approval", "POST", "/v1/documents/invalid/review/approve", "SERVICE_ACCOUNT", []string{"BlixAdmins"}, 403},
		{"service account document rejection", "POST", "/v1/documents/invalid/review/reject", "SERVICE_ACCOUNT", []string{"BlixUsers"}, 403},
		{"anonymous rejection", "POST", "/v1/changesets/invalid/review/reject", "", nil, 401},
		{"nonmember rejection", "POST", "/v1/changesets/invalid/review/reject", "USER", nil, 403},
		{"user rejection", "POST", "/v1/changesets/invalid/review/reject", "USER", []string{"BlixUsers"}, 400},
		{"user approval", "POST", "/v1/changesets/invalid/review/approve", "USER", []string{"BlixUsers"}, 403},
		{"admin approval", "POST", "/v1/changesets/invalid/review/approve", "USER", []string{"BlixAdmins"}, 400},
		{"admin rejection", "POST", "/v1/changesets/invalid/review/reject", "USER", []string{"BlixAdmins"}, 400},
		{"service account approval", "POST", "/v1/changesets/invalid/review/approve", "SERVICE_ACCOUNT", []string{"BlixAdmins"}, 403},
		{"service account rejection", "POST", "/v1/changesets/invalid/review/reject", "SERVICE_ACCOUNT", []string{"BlixUsers"}, 403},
		{"user changeset read", "GET", "/v1/changesets/invalid", "USER", []string{"BlixUsers"}, 400},
		{"user base read", "GET", "/v1/changesets/invalid/base", "USER", []string{"BlixUsers"}, 400},
		{"admin base read", "GET", "/v1/changesets/invalid/base", "USER", []string{"BlixAdmins"}, 400},
		{"nonmember base read", "GET", "/v1/changesets/invalid/base", "USER", nil, 403},
		{"user admin list", "GET", "/v1/changesets?limit=invalid", "USER", []string{"BlixUsers"}, 403},
		{"admin list", "GET", "/v1/changesets?limit=invalid", "USER", []string{"BlixAdmins"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"note":"Missing exception"}`))
			request.Header.Set("Content-Type", "application/json")
			if tc.entityType != "" {
				claims := jwt.MapClaims{"aud": "blix-test", "sub": identifier.New(identifier.Entity), "entity_type": tc.entityType, "groups": tc.groups, "exp": time.Now().Add(time.Minute).Unix()}
				if tc.entityType == "USER" {
					claims["user_id"] = identifier.New(identifier.User)
				} else {
					claims["service_account_id"] = identifier.New(identifier.ServiceAccount)
				}
				token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
				token.Header["kid"] = "review-test"
				signed, err := token.SignedString(key)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+signed)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d, expected=%d, body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}
