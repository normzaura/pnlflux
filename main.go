package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	pnlfluxHandler "github.com/normzaura/pnlflux/handlers"
	"github.com/normzaura/pnlflux/util"
)

func main() {
	startServer()
}

func startServer() {
	cfg := util.LoadConfig()

	pnlfluxHandler.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	pnlfluxHandler.DoubleBase = cfg.DoubleBase
	pnlfluxHandler.HttpClient = &http.Client{Timeout: 10 * time.Second}
	pnlfluxHandler.Tokens = util.NewTokenProvider(pnlfluxHandler.HttpClient, cfg.DoubleBase+"/oauth/token", cfg.ClientID, cfg.ClientSecret)
	pnlfluxHandler.SupabaseURL = cfg.SupabaseURL
	pnlfluxHandler.SupabaseKey = cfg.SupabaseKey

	s3Client, err := util.NewS3Client(context.Background(), cfg.S3Bucket)
	if err != nil {
		log.Fatalf("failed to create s3 client: %v", err)
	}
	pnlfluxHandler.S3 = s3Client

	r := gin.Default()

	r.POST("/webhooks/financialsflux", pnlfluxHandler.HandleFinancialsFlux)
	r.GET("/updatemonthstatus", pnlfluxHandler.HandleExportEndCloses)

	log.Println("Server running on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
