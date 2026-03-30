package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/normzaura/pnlflux/util"
)

var (
	Logger        *slog.Logger
	HttpClient    *http.Client
	DoubleBase    string
	Tokens        *util.TokenProvider
	CategoryNames map[string]float64
	S3            *util.S3Client
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

// HandleFinancialsFlux is triggered when Double HQ starts a
// "Pre Manager Financial Analysis" zapData, signalling that a financial
// report is ready for flux analysis.
//
// Route: POST /webhooks/financialsflux
func HandleFinancialsFlux(c *gin.Context) {
	rawBody, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(rawBody))
	Logger.Info("raw zapier payload", "body", string(rawBody))

	var zapData zapierTaskPayload

	if err := c.ShouldBindJSON(&zapData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	clientID, err := strconv.Atoi(zapData.ClientID)
	if err != nil {
		Logger.Error("invalid clientId in payload", "client_id", zapData.ClientID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid clientId"})
		return
	}

	doubleTaskID, err := strconv.Atoi(strings.TrimSpace(zapData.TaskID))
	if err != nil {
		Logger.Error("invalid zapDataId in payload", "raw_task_id", zapData.TaskID, "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zapDataId"})
		return
	}

	Logger.Info("financials flux webhook received",
		"doubleTask_id", doubleTaskID,
		"name", zapData.Name,
		"status", zapData.NewStatus,
		"client_id", clientID,
	)

	c.JSON(http.StatusOK, gin.H{"received": true})

	go processFinancialsFlux(clientID, doubleTaskID, zapData.Name, zapData.ClientName)
}

func processFinancialsFlux(clientID, doubleTaskID int, name, clientName string) {
	defer func() {
		if r := recover(); r != nil {
			Logger.Error("panic in processFinancialsFlux", "recover", r, "doubleTask_id", doubleTaskID)
		}
	}()

	ctx := context.Background()

	if !strings.EqualFold(name, "automated review") {
		Logger.Info("ignoring zapData, name is not 'automated review'", "name", name)
		return
	}

	bufferSecs := 20 // DEFAULT VALUE, REPLACED BY ENV SET VALUE
	if v, err := strconv.Atoi(os.Getenv("POST_REQUEST_BUFFER")); err == nil && v > 0 {
		bufferSecs = v
	} else {
		Logger.Warn("POST_REQUEST_BUFFER not set or invalid, using default", "default_secs", bufferSecs)
	}
	time.Sleep(time.Duration(bufferSecs) * time.Second)

	files, err := util.FetchClientFiles(ctx, HttpClient, DoubleBase, Tokens, clientID, doubleTaskID)
	if err != nil {
		Logger.Error("failed to fetch client files", "client_id", clientID, "doubleTask_id", doubleTaskID, "err", err)
		return
	}

	if len(files.TaskAttachments) == 0 {
		Logger.Info("zapData has no attached files", "zapData_name", name, "doubleTask_id", doubleTaskID)
		return
	}

	Logger.Info("fetched client files",
		"client_id", clientID,
		"doubleTask_id", doubleTaskID,
		"zapData_attachment_count", len(files.TaskAttachments),
	)

	results, err := util.DownloadAndProcess(ctx, HttpClient, files.TaskAttachments, CategoryNames)
	if err != nil {
		Logger.Error("failed to download and process financials", "client_id", clientID, "doubleTask_id", doubleTaskID, "err", err)
		return
	}

	decodedClientName, err := url.PathUnescape(clientName)
	if err != nil {
		Logger.Warn("failed to decode clientName, using raw value", "clientName", clientName, "err", err)
		decodedClientName = clientName
	}
	safeClientName := strings.NewReplacer(
		" ", "-",
		",", "",
		".", "",
		"'", "",
		"&", "",
	).Replace(decodedClientName)

	for fileName, data := range results {
		key := fmt.Sprintf("processed/%s/%s", safeClientName, fileName)
		objectURL, err := S3.PushToS3(ctx, key, data)
		if err != nil {
			Logger.Error("failed to upload to s3", "client_id", clientID, "file", fileName, "err", err)
			continue
		}
		Logger.Info("uploaded processed file to s3", "client_id", clientID, "doubleTask_id", doubleTaskID, "url", objectURL)

		if err := util.PatchTaskSubText(ctx, HttpClient, DoubleBase, Tokens, doubleTaskID, objectURL); err != nil {
			Logger.Error("failed to patch zapData subtext", "doubleTask_id", doubleTaskID, "err", err)
		} else {
			Logger.Info("patched zapData subtext with s3 url", "doubleTask_id", doubleTaskID)
		}
	}
}
