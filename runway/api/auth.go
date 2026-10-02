package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

const (
	callerContextKey = "caller"
	accessTokenKey   = "access_token"
)

type authorizationCodeRequest struct {
	Code        string `json:"code" binding:"required"`
	RedirectURI string `json:"redirect_uri" binding:"required"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func RequireAuthentication(auth *service.AuthService) gin.HandlerFunc {
	return requireAuthentication(auth, false)
}

func RequireStreamAuthentication(auth *service.AuthService) gin.HandlerFunc {
	return requireAuthentication(auth, true)
}

func requireAuthentication(auth *service.AuthService, allowQueryToken bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorization := c.GetHeader("Authorization")
		accessToken := ""
		if strings.HasPrefix(authorization, "Bearer ") {
			accessToken = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		} else if allowQueryToken {
			accessToken = strings.TrimSpace(c.Query("access_token"))
		}
		if accessToken == "" {
			respondError(c, service.ErrUnauthorized)
			c.Abort()
			return
		}
		caller, err := auth.Authenticate(c.Request.Context(), accessToken)
		if err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		c.Set(callerContextKey, caller)
		c.Set(accessTokenKey, accessToken)
		c.Next()
	}
}

func RequireGroup(groups ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		for _, callerGroup := range caller.Groups {
			if slices.Contains(groups, callerGroup) {
				c.Next()
				return
			}
		}
		respondError(c, service.ErrForbidden)
		c.Abort()
	}
}

func (h *Handler) GetAuthConfiguration(c *gin.Context) {
	authorizationURL, err := h.auth.AuthorizationURL(c.Query("state"), c.Query("redirect_uri"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authorization_url": authorizationURL,
	})
}

func (h *Handler) LoginWithSentinel(c *gin.Context) {
	var request authorizationCodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	token, err := h.auth.ExchangeAuthorizationCode(c.Request.Context(), request.Code, request.RedirectURI)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, token)
}

func (h *Handler) RefreshSession(c *gin.Context) {
	var request refreshTokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	token, err := h.auth.RefreshToken(c.Request.Context(), request.RefreshToken)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, token)
}

func (h *Handler) GetSession(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, caller)
}

func (h *Handler) GetCurrentUser(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	accessToken, err := getAccessToken(c)
	if err != nil {
		respondError(c, err)
		return
	}
	user, err := h.auth.GetCurrentUser(c.Request.Context(), accessToken, caller)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, user)
}

func (h *Handler) Logout(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

func getCaller(c *gin.Context) (service.CallerContext, error) {
	value, exists := c.Get(callerContextKey)
	if !exists {
		return service.CallerContext{}, service.ErrUnauthorized
	}
	caller, ok := value.(service.CallerContext)
	if !ok || caller.EntityID == "" {
		return service.CallerContext{}, service.ErrUnauthorized
	}
	return caller, nil
}

func getAccessToken(c *gin.Context) (string, error) {
	value, exists := c.Get(accessTokenKey)
	if !exists {
		return "", service.ErrUnauthorized
	}
	accessToken, ok := value.(string)
	if !ok || strings.TrimSpace(accessToken) == "" {
		return "", service.ErrUnauthorized
	}
	return accessToken, nil
}
