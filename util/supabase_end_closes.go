package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type EndCloseRow struct {
	ID         int     `json:"id"`
	ClientID   int     `json:"client_id"`
	ClientName string  `json:"client_name"`
	YearMonth  string  `json:"year_month"`
	Status     string  `json:"status"`
	Progress   string  `json:"progress"`
	DueDate    *string `json:"due_date"`
}

// UpsertEndCloses bulk-upserts end close rows into the end_closes table.
// Conflicts on id are resolved by updating the existing row.
func UpsertEndCloses(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string, rows []EndCloseRow) error {
	if len(rows) == 0 {
		return nil
	}

	body, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal end closes: %w", err)
	}

	upsertURL := fmt.Sprintf("%s/rest/v1/end_closes", supabaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upsertURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build upsert request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=minimal")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upsert end closes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert end closes returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
