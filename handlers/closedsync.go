package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/normzaura/pnlflux/util"
)

func HandleClosedSync(c *gin.Context) {
	c.JSON(http.StatusAccepted, gin.H{"message": "end-closes export started"})
	go runEndClosesExport()
}

func runEndClosesExport() {
	defer func() {
		if r := recover(); r != nil {
			Logger.Error("panic in runEndClosesExport", "recover", r)
		}
	}()

	ctx := context.Background()

	const testClientID = 511616
	closes, err := util.FetchClientEndCloses(ctx, HttpClient, DoubleBase, Tokens, testClientID)
	if err != nil {
		Logger.Error("end-closes export: failed to fetch end-closes", "client_id", testClientID, "err", err)
		return
	}
	Logger.Info("end-closes export: fetched closes", "client_id", testClientID, "count", len(closes))
	if len(closes) > 0 {
		if err := util.SyncClientClosed(ctx, HttpClient, ClosedSupabaseURL, ClosedSupabaseKey, testClientID, closes); err != nil {
			Logger.Error("end-closes export: failed to sync closed", "client_id", testClientID, "err", err)
			return
		}
		Logger.Info("end-closes export: synced client", "client_id", testClientID, "count", len(closes))
	}

	Logger.Info("end-closes export: complete")
}
