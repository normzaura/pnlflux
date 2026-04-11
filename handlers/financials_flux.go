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
	"regexp"
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
	SpecialTerms  map[string]float64
	S3            *util.S3Client
)

// zapierTaskPayload matches the exact JSON Zapier sends on a
// Double HQ "Task Status Update" trigger. All numeric IDs arrive as strings.
type zapierTaskPayload struct {
	TaskID         string `json:"taskId"`
	DoubleTaskName string `json:"name"`
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

// The process starts from a Zapier POST
func HandleFinancialsFlux(c *gin.Context) {
	rawBody, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(rawBody))
	Logger.Info("raw zapier payload", "body", string(rawBody))

	// Process Zapier payload
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
		"name", zapData.DoubleTaskName,
		"status", zapData.NewStatus,
		"client_id", clientID,
	)

	if !strings.EqualFold(zapData.DoubleTaskName, "automated review") {
		Logger.Info("ignoring double task, task name is not 'automated review'", "name", zapData.DoubleTaskName)
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	if !strings.EqualFold(zapData.NewStatus, "In Progress") {
		Logger.Info("ignoring double task, status is not 'In Progress'", "status", zapData.NewStatus)
		c.JSON(http.StatusOK, gin.H{"received": true})
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})

	go processZapierPost(clientID, doubleTaskID, zapData.ClientName)
}

func processZapierPost(clientID, doubleTaskID int, clientName string) {
	defer func() {
		if r := recover(); r != nil {
			Logger.Error("panic in processZapierPost", "recover", r, "doubleTask_id", doubleTaskID)
		}
	}()

	ctx := context.Background()

	bufferSecs := 20 // DEFAULT VALUE, REPLACED BY ENV SET VALUE
	if v, err := strconv.Atoi(os.Getenv("POST_REQUEST_BUFFER")); err == nil && v > 0 {
		bufferSecs = v
	} else {
		Logger.Warn("POST_REQUEST_BUFFER not set or invalid, using default", "default_secs", bufferSecs)
	}
	time.Sleep(time.Duration(bufferSecs) * time.Second)

	files, err := util.FilterClientAttachedFiles(ctx, HttpClient, DoubleBase, Tokens, clientID, doubleTaskID)
	if err != nil {
		Logger.Error("failed to fetch client files", "client_id", clientID, "doubleTask_id", doubleTaskID, "err", err)
		return
	}

	if len(files.TaskAttachments) == 0 {
		Logger.Info("zapData has no attached files", "doubleTask_id", doubleTaskID)
		return
	}

	Logger.Info("fetched client files",
		"client_id", clientID,
		"doubleTask_id", doubleTaskID,
		"zapData_attachment_count", len(files.TaskAttachments),
	)

	results, logs, statsMap, tbRows, err := util.DownloadAndProcess(ctx, HttpClient, files.TaskAttachments, CategoryNames, SpecialTerms)
	if err != nil {
		Logger.Error("failed to download and process financials", "client_id", clientID, "doubleTask_id", doubleTaskID, "err", err)
		return
	}
	if tbRows != nil {
		Logger.Info("loaded TB Match workbook", "client_id", clientID, "doubleTask_id", doubleTaskID, "row_count", len(tbRows))
	}

	for fileName, data := range results {
		folder := fmt.Sprintf("processed/%s", cleanClientName(clientName))
		key := fmt.Sprintf("%s/%s", folder, fileName)
		objectURL, err := S3.PushToS3(ctx, key, data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		if err != nil {
			Logger.Error("failed to upload to s3", "client_id", clientID, "file", fileName, "err", err)
			continue
		}
		Logger.Info("uploaded processed file to s3", "client_id", clientID, "doubleTask_id", doubleTaskID, "url", objectURL)

		if !strings.EqualFold(os.Getenv("TEST"), "true") {
			if logBytes, ok := logs[fileName]; ok && len(logBytes) > 0 {
				base := strings.TrimSuffix(fileName, ".xlsx")
				logKey := fmt.Sprintf("%s/%s_log", folder, base)
				if _, err := S3.PushToS3(ctx, logKey, logBytes, "text/plain"); err != nil {
					Logger.Error("failed to upload log to s3", "client_id", clientID, "file", fileName, "err", err)
				} else {
					Logger.Info("uploaded process log to s3", "client_id", clientID, "key", logKey)
				}
			}
		}

		stats := statsMap[fileName]
		bsPart := fmt.Sprintf("[%d] inconsistent", stats.Inconsistent)
		if stats.Inconsistent == 0 {
			bsPart = "CLEAN"
		}
		pnlPart := fmt.Sprintf("[%d] missing, [%d] Flux", stats.Missing, stats.Flux)
		if stats.Missing == 0 && stats.Flux == 0 {
			pnlPart = "CLEAN"
		}
		subText := fmt.Sprintf(
			"Balance Sheet - %s  ||  PNL - %s\n\n-------------------------------------------------------------\n\n%s",
			bsPart, pnlPart, objectURL,
		)
		if err := util.PatchTaskSubText(ctx, HttpClient, DoubleBase, Tokens, doubleTaskID, subText); err != nil {
			Logger.Error("failed to patch zapData subtext", "doubleTask_id", doubleTaskID, "err", err)
		} else {
			Logger.Info("patched zapData subtext with s3 url", "doubleTask_id", doubleTaskID)
		}
	}
}

func cleanClientName(clientName string) string {
	decoded, err := url.PathUnescape(clientName)

	if err != nil {
		decoded = clientName
	}
	safe := strings.NewReplacer(
		"+", "-",
		" ", "-",
		",", "",
		".", "",
		"'", "",
		"&", "",
	).Replace(decoded)

	multiDash := regexp.MustCompile(`-{2,}`)

	return multiDash.ReplaceAllString(safe, "-")

}
