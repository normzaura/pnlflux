package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type ClosedRow struct {
	Status    string `json:"status"`
	CompanyID string `json:"company_id"`
	Date      string `json:"date"` // YYYY-MM-DD
}

// SyncClientClosed replaces all closed rows for a company with fresh data from Double HQ.
// It deletes existing rows for the company then inserts the new set.
func SyncClientClosed(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string, companyID int, closes []EndCloseSummary) error {
	if err := deleteClientClosed(ctx, httpClient, supabaseURL, supabaseKey, companyID); err != nil {
		return err
	}

	var rows []ClosedRow
	for _, ec := range closes {
		date, err := yearMonthToDate(ec.YearMonth)
		if err != nil {
			continue
		}
		rows = append(rows, ClosedRow{
			Status:    ec.Status,
			CompanyID: fmt.Sprintf("%d", companyID),
			Date:      date,
		})
	}

	if len(rows) == 0 {
		return nil
	}
	return insertClosed(ctx, httpClient, supabaseURL, supabaseKey, rows)
}

func deleteClientClosed(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string, companyID int) error {
	deleteURL := fmt.Sprintf("%s/rest/v1/closed?company_id=eq.%s", supabaseURL, url.QueryEscape(fmt.Sprintf("%d", companyID)))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Prefer", "return=minimal")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete closed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete closed returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func insertClosed(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string, rows []ClosedRow) error {
	body, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal closed rows: %w", err)
	}

	insertURL := fmt.Sprintf("%s/rest/v1/closed", supabaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, insertURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build insert request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("insert closed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("insert closed returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// UpsertClosedRow updates the status for an existing company+date row, or inserts it if absent.
func UpsertClosedRow(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, companyID, date, status string) error {
	body, err := json.Marshal(map[string]string{"status": status})
	if err != nil {
		return fmt.Errorf("marshal patch body: %w", err)
	}

	patchURL := fmt.Sprintf("%s/rest/v1/closed?company_id=eq.%s&date=eq.%s",
		supabaseURL, url.QueryEscape(companyID), url.QueryEscape(date))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, patchURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build patch request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=representation")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch closed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch closed returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var updated []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		return fmt.Errorf("decode patch response: %w", err)
	}
	if len(updated) > 0 {
		return nil
	}

	return insertClosed(ctx, httpClient, supabaseURL, supabaseKey, []ClosedRow{
		{Status: status, CompanyID: companyID, Date: date},
	})
}

// YearMonthToDate converts "MM-YYYY" to "YYYY-MM-26" for Postgres date columns.
func YearMonthToDate(yearMonth string) (string, error) {
	if len(yearMonth) == 7 && yearMonth[2] == '-' {
		return yearMonth[3:] + "-" + yearMonth[:2] + "-26", nil
	}
	return "", fmt.Errorf("unrecognized yearMonth format: %s", yearMonth)
}

func yearMonthToDate(yearMonth string) (string, error) {
	return YearMonthToDate(yearMonth)
}
