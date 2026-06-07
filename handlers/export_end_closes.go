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
		if len(closes) == 0 {
			continue
		}
		if err := util.SyncClientClosed(ctx, HttpClient, SupabaseURL, SupabaseKey, client.ID, closes); err != nil {
			Logger.Error("end-closes export: failed to sync closed", "client_id", client.ID, "client_name", client.Name, "err", err)
			continue
		}
		Logger.Info("end-closes export: synced client", "client_id", client.ID, "client_name", client.Name, "count", len(closes))
	}

	Logger.Info("end-closes export: complete")
}
