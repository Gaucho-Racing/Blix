package api

import (
	"net/http"
	"strconv"

	"github.com/gaucho-racing/blix/knowledge/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListWikiPages(c *gin.Context) {
	result, err := h.pages.ListIndexedPages(c.Request.Context(), c.Query("prefix"), c.Query("after"))
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
	options := service.WikiReadOptions{CommitSHA: c.Query("commit_sha")}
	for name, target := range map[string]*int{"cursor": &options.Cursor, "max_bytes": &options.MaxBytes} {
		if c.Query(name) != "" {
			value, err := strconv.Atoi(c.Query(name))
			if err != nil {
				respondError(c, service.ErrInvalid)
				return
			}
			*target = value
		}
	}
	if c.Query("section") != "" {
		value, err := strconv.Atoi(c.Query("section"))
		if err != nil {
			respondError(c, service.ErrInvalid)
			return
		}
		options.Section = &value
	}
	result, err := h.pages.ReadIndexedPage(c.Request.Context(), c.Query("path"), options)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) SearchWiki(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "8"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.SearchIndexed(c.Request.Context(), service.WikiSearchOptions{Query: c.Query("q"), Limit: limit, Season: c.Query("season"), Prefix: c.Query("prefix")})
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
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

func (h *Handler) SearchPendingWikiChangesets(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "8"))
	if err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.SearchPendingChangesets(c.Request.Context(), c.Query("q"), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"changesets": result})
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
		Caller: caller,
		Note:   request.Note,
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

func (h *Handler) SearchWikiBatch(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var input service.WikiSearchBatch
	if err := c.ShouldBindJSON(&input); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.SearchBatch(c.Request.Context(), input)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) ReadWikiPageBatch(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var input service.WikiPageBatch
	if err := c.ShouldBindJSON(&input); err != nil {
		respondError(c, service.ErrInvalid)
		return
	}
	result, err := h.pages.ReadPageBatch(c.Request.Context(), input)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
