package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	pnlfluxHandler "github.com/normzaura/pnlflux/handlers"
	"github.com/normzaura/pnlflux/util"
)

func main() {
	cfg := util.LoadConfig()

	pnlfluxHandler.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	pnlfluxHandler.DoubleBase = cfg.DoubleBase
	pnlfluxHandler.HttpClient = &http.Client{Timeout: 10 * time.Second}
	pnlfluxHandler.Tokens = util.NewTokenProvider(pnlfluxHandler.HttpClient, cfg.DoubleBase+"/oauth/token", cfg.ClientID, cfg.ClientSecret)

	var categoryNames map[string]float64
	if strings.EqualFold(os.Getenv("TEST"), "true") {
		log.Println("TEST mode: using categories_index + categories_index_removed with random thresholds")
		var err error
		categoryNames, err = util.LoadCategoryNamesTestMode()
		if err != nil {
			log.Fatalf("failed to load test category names: %v", err)
		}
	} else {
		var err error
		categoryNames, err = util.LoadCategoryNamesFromXLSX("categories_index.xlsx")
		if err != nil {
			log.Fatalf("failed to load category names from xlsx: %v", err)
		}
	}
	pnlfluxHandler.CategoryNames = categoryNames

	specialTerms, err := util.LoadSpecialTermsFromXLSX("special_terms.xlsx")
	if err != nil {
		log.Fatalf("failed to load special terms from xlsx: %v", err)
	}
	pnlfluxHandler.SpecialTerms = specialTerms

	s3Client, err := util.NewS3Client(context.Background(), cfg.S3Bucket)
	if err != nil {
		log.Fatalf("failed to create s3 client: %v", err)
	}
	pnlfluxHandler.S3 = s3Client

	r := gin.Default()

	r.POST("/webhooks/financialsflux", pnlfluxHandler.HandleFinancialsFlux)

	log.Println("Server running on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
