package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	pnlfluxHandler "github.com/normzaura/pnlflux/handlers"
	"github.com/normzaura/pnlflux/util"
)

func main() {
	// Local processing mode: go run main.go <financials.xlsx> [workbook.xlsx]
	if len(os.Args) > 1 {
		financialPath := os.Args[1]
		var workbookPath string
		if len(os.Args) >= 3 {
			workbookPath = os.Args[2]
		}

		isTest := strings.EqualFold(os.Getenv("TEST"), "true")

		categoryNames, err := util.LoadCategoryNamesTestMode()
		if err != nil {
			log.Fatalf("load categories: %v", err)
		}
		specialTerms, err := util.LoadSpecialTermsFromXLSX("special_terms.xlsx", isTest)
		if err != nil {
			log.Fatalf("load special terms: %v", err)
		}

		var tbRows [][]string
		if workbookPath != "" {
			wbData, err := os.ReadFile(workbookPath)
			if err != nil {
				log.Fatalf("read workbook %s: %v", workbookPath, err)
			}
			tbRows, err = util.LoadTBMatch(wbData)
			if err != nil {
				log.Fatalf("load tb match: %v", err)
			}
			log.Printf("loaded TB Match: %d rows", len(tbRows))
		}

		data, err := os.ReadFile(financialPath)
		if err != nil {
			log.Fatalf("read %s: %v", financialPath, err)
		}
		processed, _, err := util.ProcessFinancials(data, filepath.Base(financialPath), categoryNames, specialTerms, tbRows)
		if err != nil {
			log.Fatalf("process: %v", err)
		}
		if err := os.MkdirAll("results", 0755); err != nil {
			log.Fatalf("create results dir: %v", err)
		}
		outPath := filepath.Join("results", filepath.Base(financialPath))
		if err := os.WriteFile(outPath, processed, 0644); err != nil {
			log.Fatalf("write output: %v", err)
		}
		log.Printf("written: %s", outPath)
		return
	}

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

	specialTerms, err := util.LoadSpecialTermsFromXLSX("special_terms.xlsx", strings.EqualFold(os.Getenv("TEST"), "true"))
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
