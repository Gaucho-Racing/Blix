package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListWikiPages(c *gin.Context) {
	result, err := h.wiki.Pages(c.Request.Context(), "", c.Query("prefix"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) ReadWikiPage(c *gin.Context) {
	if c.Query("path") == "" {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.wiki.Pages(c.Request.Context(), c.Query("path"), "")
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"commit_sha": result.CommitSHA, "page": result.Pages[0]})
}

func (h *Handler) CreateWikiChangeset(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024*1024)
	var request service.CreateChangesetInput
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.CreateChangeset(c.Request.Context(), request)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) GetWikiChangeset(c *gin.Context) {
	result, err := h.pages.GetChangeset(c.Request.Context(), c.Param("changesetID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetWikiChangesetBase(c *gin.Context) {
	result, err := h.pages.GetChangeset(c.Request.Context(), c.Param("changesetID"))
	if err != nil {
		respondError(c, err)
		return
	}
	for _, file := range result.Files {
		if file.Path != c.Query("path") {
			continue
		}
		page, err := h.wiki.PageAt(c.Request.Context(), result.Changeset.BaseCommitSHA, file.Path)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
		return
	}
	respondError(c, service.ErrNotFound)
}

func (h *Handler) ListWikiChangesets(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.ListChangesets(c.Request.Context(), limit, c.Query("status"), c.Query("before"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) ReviewWikiChangeset(c *gin.Context) {
	caller, err := getCaller(c)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	var request struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.ReviewChangeset(c.Request.Context(), c.Param("changesetID"), c.Param("decision"), service.ReviewChangesetInput{
		ReviewerEntityID: caller.EntityID,
		Note:             request.Note,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) ApplyWikiChangeset(c *gin.Context) {
	result, err := h.pages.ApplyChangeset(c.Request.Context(), c.Param("changesetID"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) SeedWiki(c *gin.Context) {
	result, err := h.pages.Seed(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
