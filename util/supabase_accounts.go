package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// AccountsData holds all per-account fields loaded from the accounts table.
type AccountsData struct {
	Threshold          float64
	K                  float64
	PolicyMinThreshold float64
}

const (
	defaultK               = 1.5
	defaultPolicyMinThresh = 5.0 // percentage points
)

// LoadAccountsData fetches code, threshold, k, and policy_min_threshold from the
// accounts table in a single request, returning a map of lowercase code → AccountsData.
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
			if err := json.Unmarshal(thBytes, &d.Threshold); err != nil {
				var s string
				if json.Unmarshal(thBytes, &s) == nil {
					d.Threshold, _ = strconv.ParseFloat(strings.TrimSpace(s), 64)
				}
			}
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

// UpdateAccountThresholdStats PATCHes the computed threshold and statistics back
// to the accounts table row identified by code.
func UpdateAccountThresholdStats(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, code string, threshold, stdDev, avgDelta, minVal, maxVal, k, policyMin float64) error {
	body, err := json.Marshal(map[string]interface{}{
		"threshold":           threshold,
		"std_dev":             stdDev,
		"avg_delta_percent":   avgDelta,
		"min":                 minVal,
		"max":                 maxVal,
		"k":                   k,
		"policy_min_threshold": policyMin,
	})
	if err != nil {
		return fmt.Errorf("marshal update body: %w", err)
	}

	patchURL := fmt.Sprintf("%s/rest/v1/accounts?code=eq.%s", supabaseURL, url.QueryEscape(code))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, patchURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build update request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("update account stats: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("update account stats returned %d", resp.StatusCode)
	}
	return nil
}

// EnsureSpecialAccount ensures the accounts table row for code is present and has a threshold:
//   - No row → inserts with termThreshold and special_term=true.
//   - Row exists, threshold null/0 → patches threshold to termThreshold.
//   - Row exists with threshold already set → no-op (caller uses the existing value).
func EnsureSpecialAccount(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, code string, termThreshold float64) error {
	checkURL := fmt.Sprintf("%s/rest/v1/accounts?select=code,threshold&code=eq.%s&limit=1", supabaseURL, url.QueryEscape(code))
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

	if len(rows) == 0 {
		// Insert new row.
		body, err := json.Marshal(map[string]interface{}{
			"code":         code,
			"threshold":    termThreshold,
			"special_term": true,
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
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusCreated {
			return fmt.Errorf("insert account returned %d", resp2.StatusCode)
		}
		return nil
	}

	// Row exists — check whether threshold is already set.
	var existingThreshold float64
	if thBytes, ok := rows[0]["threshold"]; ok && string(thBytes) != "null" {
		json.Unmarshal(thBytes, &existingThreshold) //nolint:errcheck — zero value on failure is correct
	}
	if existingThreshold > 0 {
		return nil // threshold already set, nothing to do
	}

	// Patch threshold only.
	body, err := json.Marshal(map[string]interface{}{"threshold": termThreshold})
	if err != nil {
		return fmt.Errorf("marshal patch body: %w", err)
	}
	patchURL := fmt.Sprintf("%s/rest/v1/accounts?code=eq.%s", supabaseURL, url.QueryEscape(code))
	req, err = http.NewRequestWithContext(ctx, http.MethodPatch, patchURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build patch request: %w", err)
	}
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	resp2, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch account threshold: %w", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		return fmt.Errorf("patch account threshold returned %d", resp2.StatusCode)
	}
	return nil
}
