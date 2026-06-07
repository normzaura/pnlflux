package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/normzaura/pnlflux/util"
)

type updateClosedPayload struct {
	ClientID  string `json:"clientId"`
	YearMonth string `json:"yearMonth"` // MM-YYYY
	Status    string `json:"status"`
}

func HandleClosedUpdate(c *gin.Context) {
	var payload updateClosedPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	if strings.TrimSpace(payload.ClientID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clientId is required"})
		return
	}
	if strings.TrimSpace(payload.Status) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}

	date, err := util.YearMonthToDate(payload.YearMonth)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid yearMonth: %s", err)})
		return
	}

	ctx := c.Request.Context()
	if err := util.UpsertClosedRow(ctx, HttpClient, ClosedSupabaseURL, ClosedSupabaseKey, payload.ClientID, date, payload.Status); err != nil {
		Logger.Error("failed to upsert closed row",
			"client_id", payload.ClientID,
			"year_month", payload.YearMonth,
			"err", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update closed"})
		return
	}

	Logger.Info("closed row upserted", "client_id", payload.ClientID, "year_month", payload.YearMonth, "status", payload.Status)
	c.JSON(http.StatusOK, gin.H{"updated": true})
}
