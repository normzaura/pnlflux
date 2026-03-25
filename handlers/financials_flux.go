package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	categories []util.Category
	s3         *util.S3Client
}

// NewWebhookHandler constructs a WebhookHandler with the given logger and Double HQ credentials.
func NewWebhookHandler(logger *slog.Logger, httpClient *http.Client, doubleBase string, tokens *util.TokenProvider, categories []util.Category, s3 *util.S3Client) *WebhookHandler {
	return &WebhookHandler{
		logger:     logger,
		httpClient: httpClient,
		doubleBase: doubleBase,
		tokens:     tokens,
		categories: categories,
		s3:         s3,
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

	taskID, err := strconv.Atoi(task.TaskID)
	if err != nil {
		h.logger.Error("invalid taskId in payload", "task_id", task.TaskID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid taskId"})
		return
	}

	h.logger.Info("financials flux webhook received",
		"task_id", taskID,
		"name", task.Name,
		"status", task.NewStatus,
		"client_id", clientID,
	)

	c.JSON(http.StatusOK, gin.H{"received": true})

	go h.processFinancialsFlux(clientID, taskID, task.Name, task.ClientName)
}

func (h *WebhookHandler) processFinancialsFlux(clientID, taskID int, name, clientName string) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("panic in processFinancialsFlux", "recover", r, "task_id", taskID)
		}
	}()

	ctx := context.Background()

	if !strings.EqualFold(name, "automated review") {
		h.logger.Info("ignoring task, name is not 'automated review'", "name", name)
		return
	}

	time.Sleep(20 * time.Second)

	files, err := util.FetchClientFiles(ctx, h.httpClient, h.doubleBase, h.tokens, clientID, taskID)
	if err != nil {
		h.logger.Error("failed to fetch client files", "client_id", clientID, "task_id", taskID, "err", err)
		return
	}

	if len(files.TaskAttachments) == 0 {
		h.logger.Info("task has no attached files", "task_name", name, "task_id", taskID)
		return
	}

	h.logger.Info("fetched client files",
		"client_id", clientID,
		"task_id", taskID,
		"task_attachment_count", len(files.TaskAttachments),
	)

	results, err := util.DownloadAndProcess(ctx, h.httpClient, files.TaskAttachments, h.categories)
	if err != nil {
		h.logger.Error("failed to download and process financials", "client_id", clientID, "task_id", taskID, "err", err)
		return
	}

	for fileName, data := range results {
		key := fmt.Sprintf("processed/%s/%s", clientName, fileName)
		objectURL, err := h.s3.PushToS3(ctx, key, data)
		if err != nil {
			h.logger.Error("failed to upload to s3", "client_id", clientID, "file", fileName, "err", err)
			continue
		}
		h.logger.Info("uploaded processed file to s3", "client_id", clientID, "task_id", taskID, "url", objectURL)

		if err := util.PatchTaskSubText(ctx, h.httpClient, h.doubleBase, h.tokens, taskID, objectURL); err != nil {
			h.logger.Error("failed to patch task subtext", "task_id", taskID, "err", err)
		} else {
			h.logger.Info("patched task subtext with s3 url", "task_id", taskID)
		}
	}
}
