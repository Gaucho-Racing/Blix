package api

import (
	"strings"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

const callerContextKey = "caller"

func RequireAuthentication(auth *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		authorization := c.GetHeader("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			respondError(c, service.ErrUnauthorized)
			c.Abort()
			return
		}
		caller, err := auth.Authenticate(c.Request.Context(), strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
		if err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		c.Set(callerContextKey, caller)
		c.Next()
	}
}

func RequireGroup(group string) gin.HandlerFunc {
	return func(c *gin.Context) {
		caller, err := getCaller(c)
		if err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		for _, callerGroup := range caller.Groups {
			if callerGroup == group {
				c.Next()
				return
			}
		}
		respondError(c, service.ErrForbidden)
		c.Abort()
	}
}

func getCaller(c *gin.Context) (service.CallerContext, error) {
	value, exists := c.Get(callerContextKey)
	caller, ok := value.(service.CallerContext)
	if !exists || !ok || caller.EntityID == "" {
		return service.CallerContext{}, service.ErrUnauthorized
	}
	return caller, nil
}
