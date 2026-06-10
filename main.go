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
	doubleBase := os.Getenv("DOUBLE_BASE_URL")
	if doubleBase == "" {
		doubleBase = "https://api.doublehq.com"
	}

	pnlfluxHandler.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	pnlfluxHandler.DoubleBase = doubleBase
	pnlfluxHandler.HttpClient = &http.Client{Timeout: 10 * time.Second}
	pnlfluxHandler.Tokens = util.NewTokenProvider(pnlfluxHandler.HttpClient, doubleBase+"/oauth/token", os.Getenv("DOUBLE_CLIENT_ID"), os.Getenv("DOUBLE_CLIENT_SECRET"))
	pnlfluxHandler.SupabaseURL = os.Getenv("SUPABASE_URL")
	pnlfluxHandler.SupabaseKey = os.Getenv("SUPABASE_KEY")
	pnlfluxHandler.ClosedSupabaseURL = os.Getenv("CLOSED_SUPABASE_URL")
	pnlfluxHandler.ClosedSupabaseKey = os.Getenv("CLOSED_SUPABASE_KEY")

	s3Client, err := util.NewS3Client(context.Background(), os.Getenv("AWS_S3_BUCKET"))
	if err != nil {
		log.Fatalf("failed to create s3 client: %v", err)
	}
	pnlfluxHandler.S3 = s3Client

	r := gin.Default()

	r.POST("/webhooks/financialsflux", pnlfluxHandler.HandleFinancialsFlux)
	r.GET("/webhooks/closedsync", pnlfluxHandler.HandleClosedSync)

	log.Println("Server running on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
