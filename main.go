package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	handler "github.com/normzaura/pnlflux/handlers"
	"github.com/normzaura/pnlflux/util"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	doubleBase := os.Getenv("DOUBLE_HQ_BASE_URL")
	clientID := os.Getenv("DOUBLE_CLIENT_ID")
	clientSecret := os.Getenv("DOUBLE_CLIENT_SECRET")

	if doubleBase == "" {
		doubleBase = "https://api.doublehq.com"
	}
	if clientID == "" {
		log.Fatal("DOUBLE_CLIENT_ID is required")
	}
	if clientSecret == "" {
		log.Fatal("DOUBLE_CLIENT_SECRET is required")
	}

	categoriesPath := os.Getenv("CATEGORIES_CSV_PATH")
	if categoriesPath == "" {
		categoriesPath = "categories_index.csv"
	}
	categories, err := util.LoadCategories(categoriesPath)
	if err != nil {
		log.Fatalf("failed to load categories: %v", err)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	tokens := util.NewTokenProvider(httpClient, doubleBase+"/oauth/token", clientID, clientSecret)

	webhookHandler := handler.NewWebhookHandler(logger, httpClient, doubleBase, tokens, categories)

	r := gin.Default()

	r.POST("/webhooks/financialsflux", webhookHandler.HandleFinancialsFlux)

	log.Println("Server running on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
