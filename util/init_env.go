package util

import (
	"fmt"
	"log"
	"os"
)

// AppConfig holds all environment-derived configuration for the service.
type AppConfig struct {
	DoubleBase    string
	ClientID      string
	ClientSecret  string
	S3Bucket      string
	SupabaseURL   string
	SupabaseKey   string
}

// LoadConfig reads and validates all required environment variables,
// applying defaults where applicable. Exits immediately if a required
// variable is missing.
func LoadConfig() AppConfig {
	doubleBase := os.Getenv("DOUBLE_BASE_URL")
	if doubleBase == "" {
		fmt.Println("No DOUBLE_BASE_URL environment variable set, resetting to default: api.doublehq.com")
		doubleBase = "https://api.doublehq.com"
	}

	clientID := os.Getenv("DOUBLE_CLIENT_ID")
	if clientID == "" {
		log.Fatal("DOUBLE_CLIENT_ID is required")
	}

	clientSecret := os.Getenv("DOUBLE_CLIENT_SECRET")
	if clientSecret == "" {
		log.Fatal("DOUBLE_CLIENT_SECRET is required")
	}

	s3Bucket := os.Getenv("AWS_S3_BUCKET")
	if s3Bucket == "" {
		log.Fatal("AWS_S3_BUCKET is required")
	}

	supabaseURL := os.Getenv("SUPABASE_URL")
	if supabaseURL == "" {
		log.Fatal("SUPABASE_URL is required")
	}

	supabaseKey := os.Getenv("SUPABASE_KEY")
	if supabaseKey == "" {
		log.Fatal("SUPABASE_KEY is required")
	}

	return AppConfig{
		DoubleBase:   doubleBase,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		S3Bucket:     s3Bucket,
		SupabaseURL:  supabaseURL,
		SupabaseKey:  supabaseKey,
	}
}
