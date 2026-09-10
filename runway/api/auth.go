package api

import (
	"net/http"
	"strings"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

const callerContextKey = "caller"

type authorizationCodeRequest struct {
	Code string `json:"code" binding:"required"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func RequireAuthentication(auth *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorization := c.GetHeader("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			respondError(c, service.ErrUnauthorized)
			c.Abort()
			return
		}
		caller, err := auth.Authenticate(c.Request.Context(), strings.TrimPrefix(authorization, "Bearer "))
		if err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		c.Set(callerContextKey, caller)
		c.Next()
	}
}

func (h *Handler) GetAuthConfiguration(c *gin.Context) {
	authorizationURL, err := h.auth.AuthorizationURL(c.Query("state"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authorization_url": authorizationURL,
		"redirect_uri":      h.auth.RedirectURI(),
	})
}

func (h *Handler) LoginWithSentinel(c *gin.Context) {
	var request authorizationCodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	token, err := h.auth.ExchangeAuthorizationCode(c.Request.Context(), request.Code)
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
