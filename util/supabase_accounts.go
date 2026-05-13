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
	"time"
)

// ThresholdRecord is a single computed threshold value within a company entry.
type ThresholdRecord struct {
	Value        float64 `json:"value"`
	ClosingMonth string  `json:"closing_month"` // "MM-YYYY"
	CreatedOn    string  `json:"created_on"`
}

// ThresholdEntry is one element of the threshold JSONB array stored per account,
// scoped to a specific company. All historical threshold records for that company
// are nested inside Thresholds.
type ThresholdEntry struct {
	Company    CompanyInfo       `json:"company"`
	Thresholds []ThresholdRecord `json:"thresholds"`
}

type CompanyInfo struct {
	Name string `json:"name"`
}

// AccountsData holds all per-account fields loaded from the accounts table.
type AccountsData struct {
	ThresholdEntries   []ThresholdEntry
	K                  float64
	PolicyMinThreshold float64
}

const (
	defaultK               = 1.0
	defaultPolicyMinThresh = 5.0 // percentage points
)

// LoadAccountsData fetches code, threshold, k, and policy_min_threshold
// from the accounts table in a single request, returning a map of lowercase code → AccountsData.
func LoadAccountsData(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string) (map[string]AccountsData, error) {
	fetchURL := fmt.Sprintf("%s/rest/v1/accounts?select=code,threshold,k,policy_min_threshold", supabaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build accounts request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accounts request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("accounts request returned %d", resp.StatusCode)
	}

	var raw []map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode accounts response: %w", err)
	}

	accounts := make(map[string]AccountsData, len(raw))
	for _, row := range raw {
		codeBytes, ok := row["code"]
		if !ok {
			continue
		}
		var code string
		if err := json.Unmarshal(codeBytes, &code); err != nil {
			continue
		}
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			continue
		}
		var d AccountsData
		if thBytes, ok := row["threshold"]; ok && string(thBytes) != "null" {
			json.Unmarshal(thBytes, &d.ThresholdEntries) //nolint:errcheck
		}
		if kBytes, ok := row["k"]; ok && string(kBytes) != "null" {
			json.Unmarshal(kBytes, &d.K) //nolint:errcheck
		}
		if pmBytes, ok := row["policy_min_threshold"]; ok && string(pmBytes) != "null" {
			json.Unmarshal(pmBytes, &d.PolicyMinThreshold) //nolint:errcheck
		}
		accounts[code] = d
	}
	return accounts, nil
}

// PatchAccountThreshold replaces the threshold JSONB array for an account row.
func PatchAccountThreshold(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, code string, entries []ThresholdEntry) error {
	body, err := json.Marshal(map[string]interface{}{
		"threshold": entries,
	})
	if err != nil {
		return fmt.Errorf("marshal threshold body: %w", err)
	}

	patchURL := fmt.Sprintf("%s/rest/v1/accounts?code=eq.%s", supabaseURL, url.QueryEscape(code))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, patchURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build patch request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch account threshold: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("patch account threshold returned %d", resp.StatusCode)
	}
	return nil
}

func newThresholdRecord(closingMonth string, value float64) ThresholdRecord {
	return ThresholdRecord{
		Value:        value,
		ClosingMonth: closingMonth,
		CreatedOn:    time.Now().UTC().Format(time.RFC3339),
	}
}

// appendThresholdRecord adds a new ThresholdRecord to the matching company entry.
// If no entry exists for the company yet, a new one is created.
func appendThresholdRecord(entries []ThresholdEntry, companyName, closingMonth string, value float64) []ThresholdEntry {
	record := newThresholdRecord(closingMonth, value)
	for i, entry := range entries {
		if strings.EqualFold(entry.Company.Name, companyName) {
			entries[i].Thresholds = append(entries[i].Thresholds, record)
			return entries
		}
	}
	return append(entries, ThresholdEntry{
		Company:    CompanyInfo{Name: companyName},
		Thresholds: []ThresholdRecord{record},
	})
}


// Search if there is a matched special account, if not then it will insert
func lookupAndUpdateSpecialAccount(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, code string, termThreshold float64, termType, volatility string) error {
	checkURL := fmt.Sprintf("%s/rest/v1/accounts?select=code&code=eq.%s&limit=1", supabaseURL, url.QueryEscape(code))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return fmt.Errorf("build check request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("check account: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("check account returned %d", resp.StatusCode)
	}

	var rows []map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return fmt.Errorf("decode check response: %w", err)
	}

	if len(rows) > 0 {
		return nil // row already exists with a threshold set
	}

	body, err := json.Marshal(map[string]interface{}{
		"code":                 code,
		"threshold":            termThreshold,
		"k":                    defaultK,
		"policy_min_threshold": defaultPolicyMinThresh,
		"special_term":         true,
		"type":                 termType,
		"volatility":           volatility,
	})
	if err != nil {
		return fmt.Errorf("marshal insert body: %w", err)
	}
	insertURL := fmt.Sprintf("%s/rest/v1/accounts", supabaseURL)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, insertURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build insert request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	resp2, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("insert account: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("insert account returned %d: %s", resp2.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
