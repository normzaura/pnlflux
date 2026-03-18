package handler

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/normzaura/pnlflux/util"
)

// zapierTaskPayload matches the exact JSON Zapier sends on a
// Double HQ "Task Status Update" trigger. All numeric IDs arrive as strings.
type zapierTaskPayload struct {
	TaskID         string `json:"taskId"`
	Name           string `json:"name"`
	ClientID       string `json:"clientId"`
	ClientName     string `json:"clientName"`
	AssigneeID     string `json:"assigneeId"`
	AssigneeName   string `json:"assigneeName"`
	NewStatus      string `json:"newStatus"`
	OldStatus      string `json:"oldStatus"`
	Section        string `json:"section"`
	DueDate        string `json:"dueDate"`
	IsHighPriority string `json:"isHighPriority"`
	Type           string `json:"type"`
	UpdatedTime    string `json:"updatedTime"`
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
	rawBody, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(rawBody))
	h.logger.Info("raw zapier payload", "body", string(rawBody))

	var task zapierTaskPayload

	if err := c.ShouldBindJSON(&task); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	clientID, err := strconv.Atoi(task.ClientID)
	if err != nil {
		h.logger.Error("invalid clientId in payload", "client_id", task.ClientID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid clientId"})
		return
	}

	h.logger.Info("financials flux webhook received",
		"task_id", task.TaskID,
		"name", task.Name,
		"status", task.NewStatus,
		"client_id", clientID,
	)

	files, err := util.FetchClientFiles(c.Request.Context(), h.httpClient, h.doubleBase, h.tokens, clientID)
	if err != nil {
		h.logger.Error("failed to fetch client files", "client_id", task.ClientID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch client files"})
		return
	}

	h.logger.Info("fetched client files",
		"client_id", clientID,
		"root_children", len(files.Root.Children),
	)

	// TODO: pass files to analysis service

	c.JSON(http.StatusOK, gin.H{"received": true})
}
