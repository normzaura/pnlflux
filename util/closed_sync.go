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
// Closes with quarterly dates (Q1–Q4) are silently skipped.
// Delete only runs if at least one valid row exists, preventing accidental data loss.
func SyncClientClosed(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string, companyID int, closes []EndCloseSummary) error {
	var rows []ClosedRow
	for _, ec := range closes {
		date, err := yearMonthToDate(ec.YearMonth)
		if err != nil {
			return fmt.Errorf("yearMonth parse failed (raw value: %q): %w", ec.YearMonth, err)
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

	if err := DeleteClientClosed(ctx, httpClient, supabaseURL, supabaseKey, companyID); err != nil {
		return err
	}
	return insertClosed(ctx, httpClient, supabaseURL, supabaseKey, rows)
}

func isQuarterlyPeriod(yearMonth string) bool {
	return len(yearMonth) >= 2 && (yearMonth[0] == 'Q' || yearMonth[0] == 'q')
}

// IsQuarterlyPeriod reports whether a yearMonth string represents a quarterly period.
func IsQuarterlyPeriod(yearMonth string) bool {
	return isQuarterlyPeriod(yearMonth)
}

func DeleteClientClosed(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string, companyID int) error {
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

// YearMonthToDate converts "YYYYMM" (Double HQ API format) to "YYYY-MM-26" for Postgres date columns.
func YearMonthToDate(yearMonth string) (string, error) {
	if len(yearMonth) == 6 {
		return yearMonth[:4] + "-" + yearMonth[4:] + "-26", nil
	}
	return "", fmt.Errorf("unrecognized yearMonth format: %s", yearMonth)
}

func yearMonthToDate(yearMonth string) (string, error) {
	return YearMonthToDate(yearMonth)
}

// EnsureCompanyExists checks if a company exists in public.companies and creates it if not.
func EnsureCompanyExists(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, companyID, companyName string) error {
	checkURL := fmt.Sprintf("%s/rest/v1/companies?id=eq.%s&select=id&limit=1", supabaseURL, url.QueryEscape(companyID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return fmt.Errorf("build company check request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("check company: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("check company returned %d", resp.StatusCode)
	}

	var rows []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return fmt.Errorf("decode company check: %w", err)
	}
	if len(rows) > 0 {
		return nil
	}

	body, err := json.Marshal(map[string]string{
		"id":           companyID,
		"company_name": companyName,
	})
	if err != nil {
		return fmt.Errorf("marshal company: %w", err)
	}
	insertURL := fmt.Sprintf("%s/rest/v1/companies", supabaseURL)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, insertURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build company insert request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp2, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("insert company: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("insert company returned %d: %s", resp2.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
