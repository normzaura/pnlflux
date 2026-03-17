package handler

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/normzaura/pnlflux/util"
)

// zapierTaskPayload matches the Double HQ Task object that Zapier forwards
// on a "Task Status Update" trigger. Field names and types are taken directly
// from the Double HQ OpenAPI schema.
// NOTE: field names are unverified against a live Zapier sample — confirm once
// a real trigger has been tested via RequestBin or Webhook.site.
type zapierTaskPayload struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`   // notStarted | inProgress | done | customerBlocked
	Priority    *string `json:"priority"` // low | medium | high
	DueDate     *string `json:"dueDate"`
	AssigneeID  *int    `json:"assigneeId"`
	ClientID    int     `json:"clientId"`
	SectionID   *int    `json:"sectionId"`
	CreatedAt   *string `json:"createdAt"`
	UpdatedAt   *string `json:"updatedAt"`
}

// WebhookHandler handles inbound webhook events from Double HQ.
type WebhookHandler struct {
	logger     *slog.Logger
	httpClient *http.Client
	doubleBase string
	tokens     *util.TokenProvider
}

// NewWebhookHandler constructs a WebhookHandler with the given logger and Double HQ credentials.
func NewWebhookHandler(logger *slog.Logger, httpClient *http.Client, doubleBase string, tokens *util.TokenProvider) *WebhookHandler {
	return &WebhookHandler{
		logger:     logger,
		httpClient: httpClient,
		doubleBase: doubleBase,
		tokens:     tokens,
	}
}

// HandleFinancialsFlux is triggered when Double HQ starts a
// "Pre Manager Financial Analysis" task, signalling that a financial
// report is ready for flux analysis.
//
// Route: POST /webhooks/financialsflux
func (h *WebhookHandler) HandleFinancialsFlux(c *gin.Context) {
	var task zapierTaskPayload

	if err := c.ShouldBindJSON(&task); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	h.logger.Info("financials flux webhook received",
		"task_id", task.ID,
		"name", task.Name,
		"status", task.Status,
		"client_id", task.ClientID,
	)

	files, err := util.FetchClientFiles(c.Request.Context(), h.httpClient, h.doubleBase, h.tokens, task.ClientID)
	if err != nil {
		h.logger.Error("failed to fetch client files", "client_id", task.ClientID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch client files"})
		return
	}

	h.logger.Info("fetched client files",
		"client_id", task.ClientID,
		"attachment_count", len(files.Attachments),
		"folder_count", len(files.Folders),
	)

	// TODO: pass files to analysis service

	c.JSON(http.StatusOK, gin.H{"received": true})
}
