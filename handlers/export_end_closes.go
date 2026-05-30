package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/normzaura/pnlflux/util"
)

func HandleExportEndCloses(c *gin.Context) {
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

	var rows []util.EndCloseRow
	for _, client := range clients {
		closes, err := util.FetchClientEndCloses(ctx, HttpClient, DoubleBase, Tokens, client.ID)
		if err != nil {
			Logger.Error("end-closes export: failed to fetch end-closes", "client_id", client.ID, "client_name", client.Name, "err", err)
			continue
		}
		for _, ec := range closes {
			rows = append(rows, util.EndCloseRow{
				ID:         ec.ID,
				ClientID:   ec.ClientID,
				ClientName: ec.ClientName,
				YearMonth:  ec.YearMonth,
				Status:     ec.Status,
				Progress:   ec.Progress,
				DueDate:    ec.DueDate,
			})
		}
	}

	Logger.Info("end-closes export: upserting rows", "total_rows", len(rows))

	if err := util.UpsertEndCloses(ctx, HttpClient, SupabaseURL, SupabaseKey, rows); err != nil {
		Logger.Error("end-closes export: failed to upsert to supabase", "err", err)
		return
	}
	Logger.Info("end-closes export: complete", "rows_upserted", len(rows))
}
