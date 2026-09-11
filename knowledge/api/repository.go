package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetRepository(c *gin.Context) {
	state, err := h.wiki.State()
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}
