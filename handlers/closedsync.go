package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

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

	clients, err := util.FetchAllClients(ctx, HttpClient, DoubleBase, Tokens)
	if err != nil {
		Logger.Error("end-closes export: failed to fetch clients", "err", err)
		return
	}
	Logger.Info("end-closes export: fetched clients", "count", len(clients))

	for _, client := range clients {
		closes, err := util.FetchClientEndCloses(ctx, HttpClient, DoubleBase, Tokens, client.ID)
		if err != nil {
			Logger.Error("end-closes export: failed to fetch end-closes", "client_id", client.ID, "client_name", client.Name, "err", err)
			continue
		}
		var validCloses []util.EndCloseSummary
		for _, ec := range closes {
			if !util.IsQuarterlyPeriod(ec.YearMonth) {
				validCloses = append(validCloses, ec)
			}
		}
		companyID := fmt.Sprintf("%d", client.ID)
		if len(validCloses) == 0 {
			if err := util.DeleteClientClosed(ctx, HttpClient, ClosedSupabaseURL, ClosedSupabaseKey, client.ID); err != nil {
				Logger.Error("end-closes export: failed to delete quarterly-only client", "client_id", client.ID, "client_name", client.Name, "err", err)
			}
			continue
		}
		closes = validCloses
		if err := util.EnsureCompanyExists(ctx, HttpClient, ClosedSupabaseURL, ClosedSupabaseKey, companyID, client.Name); err != nil {
			Logger.Error("end-closes export: failed to ensure company", "client_id", client.ID, "client_name", client.Name, "err", err)
			continue
		}
		if err := util.SyncClientClosed(ctx, HttpClient, ClosedSupabaseURL, ClosedSupabaseKey, client.ID, closes); err != nil {
			Logger.Error("end-closes export: failed to sync closed", "client_id", client.ID, "client_name", client.Name, "err", err)
			continue
		}
		Logger.Info("end-closes export: synced client", "client_id", client.ID, "client_name", client.Name, "count", len(closes))
		time.Sleep(5 * time.Second)
	}

	Logger.Info("end-closes export: complete")
}
