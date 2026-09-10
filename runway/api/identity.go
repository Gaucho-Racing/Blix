package api

import (
	"net/http"

	"github.com/gaucho-racing/blix/runway/service"
	"github.com/gin-gonic/gin"
)

type resolveIdentitiesRequest struct {
	IDs []string `json:"ids" binding:"required"`
}

func (h *Handler) ResolveIdentities(c *gin.Context) {
	var request resolveIdentitiesRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	identities, err := h.identities.Resolve(c.Request.Context(), request.IDs)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, identities)
}
