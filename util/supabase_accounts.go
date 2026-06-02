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

type ThresholdEntry struct {
	Company    CompanyInfo      `json:"company"`
	Thresholds []ThresholdValue `json:"thresholds"`
}

type CompanyInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ThresholdValue struct {
	Value        float64 `json:"value"`         // decimal: 0.15 = 15%
	CreatedOn    string  `json:"created_on"`    // RFC3339
	ClosingMonth string  `json:"closing_month"` // "MM-YYYY"
	Confidence   int     `json:"confidence"`
	FlagRate     float64 `json:"flagrate"`
}

type HistoryEntry struct {
	Company                CompanyInfo          `json:"company"`
	AvgAbsDelta            float64              `json:"avg_absdelta"`
	CoefficientOfVariation float64              `json:"coefficient_of_variation"`
	History                []map[string]float64 `json:"history"` // each element: {"MM-YYYY": rawValue}
}

// AccountsData holds all per-account fields loaded from the accounts table.
type AccountsData struct {
	Code               string // original case as stored in the DB, used for PATCH filters
	ThresholdEntries   []ThresholdEntry // used only for preserving history when patching — never for detection
	KVal               float64
	HistoryEntries     []HistoryEntry
	PolicyMinThreshold float64
	Type               string
	Volatility         string
}


// LoadAccountsData fetches code, threshold, k, and policy_min_threshold
// from the accounts table in a single request, returning a map of lowercase code → AccountsData.
func LoadAccountsData(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey string) (map[string]AccountsData, error) {
	fetchURL := fmt.Sprintf("%s/rest/v1/accounts?select=code,threshold,policy_min_threshold,k_val,history_and_avg_absdelta,type,volatility", supabaseURL)
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
		var originalCode string
		if err := json.Unmarshal(codeBytes, &originalCode); err != nil {
			continue
		}
		originalCode = strings.TrimSpace(originalCode)
		if originalCode == "" {
			continue
		}
		code := strings.ToLower(originalCode)
		var d AccountsData
		d.Code = originalCode
		if thBytes, ok := row["threshold"]; ok && string(thBytes) != "null" {
			json.Unmarshal(thBytes, &d.ThresholdEntries) //nolint:errcheck
		}
		if pmBytes, ok := row["policy_min_threshold"]; ok && string(pmBytes) != "null" {
			json.Unmarshal(pmBytes, &d.PolicyMinThreshold) //nolint:errcheck
		}
		if kvBytes, ok := row["k_val"]; ok && string(kvBytes) != "null" {
			json.Unmarshal(kvBytes, &d.KVal) //nolint:errcheck
		}
		if hBytes, ok := row["history_and_avg_absdelta"]; ok && string(hBytes) != "null" {
			json.Unmarshal(hBytes, &d.HistoryEntries) //nolint:errcheck
		}
		if tBytes, ok := row["type"]; ok && string(tBytes) != "null" {
			json.Unmarshal(tBytes, &d.Type) //nolint:errcheck
		}
		if vBytes, ok := row["volatility"]; ok && string(vBytes) != "null" {
			json.Unmarshal(vBytes, &d.Volatility) //nolint:errcheck
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch account threshold returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// appendThresholdValue adds a new ThresholdValue for the given company into entries.
// If an entry for that company already exists its thresholds slice is extended;
// otherwise a new ThresholdEntry is appended.
func appendThresholdValue(entries []ThresholdEntry, clientID int, clientName, closingMonth string, value float64, confidence int, flagRate float64) []ThresholdEntry {
	tv := ThresholdValue{
		Value:        value,
		CreatedOn:    time.Now().UTC().Format(time.RFC3339),
		ClosingMonth: closingMonth,
		Confidence:   confidence,
		FlagRate:     flagRate,
	}
	for i, e := range entries {
		if e.Company.ID == clientID {
			entries[i].Thresholds = append(entries[i].Thresholds, tv)
			return entries
		}
	}
	return append(entries, ThresholdEntry{
		Company:    CompanyInfo{ID: clientID, Name: clientName},
		Thresholds: []ThresholdValue{tv},
	})
}



// PatchAccountHistoryAndAvgAbsDelta replaces the history_and_avg_absdelta JSONB array for an account row.
func PatchAccountHistoryAndAvgAbsDelta(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, code string, entries []HistoryEntry) error {
	body, err := json.Marshal(map[string]interface{}{
		"history_and_avg_absdelta": entries,
	})
	if err != nil {
		return fmt.Errorf("marshal history body: %w", err)
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
		return fmt.Errorf("patch history: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch history returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// upsertHistoryEntry appends a month→value entry to the company's history slice.
// If the month is already recorded it is a no-op (months are unique per company).
// If no entry exists for the company a new one is created.
func upsertHistoryEntry(entries []HistoryEntry, clientID int, clientName, monthStr string, value float64) []HistoryEntry {
	for i, e := range entries {
		if e.Company.ID == clientID {
			for _, h := range e.History {
				if _, ok := h[monthStr]; ok {
					return entries
				}
			}
			entries[i].History = append(entries[i].History, map[string]float64{monthStr: value})
			return entries
		}
	}
	return append(entries, HistoryEntry{
		Company: CompanyInfo{ID: clientID, Name: clientName},
		History: []map[string]float64{{monthStr: value}},
	})
}

// updateHistoryAvgAbsDelta sets the avg_absdelta field on the company's HistoryEntry.
func updateHistoryAvgAbsDelta(entries []HistoryEntry, clientID int, avgAbsDelta float64) []HistoryEntry {
	for i, e := range entries {
		if e.Company.ID == clientID {
			entries[i].AvgAbsDelta = avgAbsDelta
			return entries
		}
	}
	return entries
}

// updateHistoryCoefficientOfVariation sets the coefficient_of_variation field on the company's HistoryEntry.
func updateHistoryCoefficientOfVariation(entries []HistoryEntry, clientID int, cv float64) []HistoryEntry {
	for i, e := range entries {
		if e.Company.ID == clientID {
			entries[i].CoefficientOfVariation = cv
			return entries
		}
	}
	return entries
}

// Search if there is a matched special account, if not then it will insert
func lookupAndUpdateSpecialAccount(ctx context.Context, httpClient *http.Client, supabaseURL, supabaseKey, code string, k float64, termType, volatility string) error {
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
		return nil // row already exists
	}

	excludedURL := fmt.Sprintf("%s/rest/v1/excluded?select=code&code=eq.%s&limit=1", supabaseURL, url.QueryEscape(code))
	excludedReq, err := http.NewRequestWithContext(ctx, http.MethodGet, excludedURL, nil)
	if err != nil {
		return fmt.Errorf("build excluded check request: %w", err)
	}
	excludedReq.Header.Set("apikey", supabaseKey)
	excludedReq.Header.Set("Authorization", "Bearer "+supabaseKey)
	excludedReq.Header.Set("Accept", "application/json")

	excludedResp, err := httpClient.Do(excludedReq)
	if err != nil {
		return fmt.Errorf("check excluded: %w", err)
	}
	defer excludedResp.Body.Close()
	if excludedResp.StatusCode != http.StatusOK {
		return fmt.Errorf("check excluded returned %d", excludedResp.StatusCode)
	}

	var excludedRows []map[string]json.RawMessage
	if err := json.NewDecoder(excludedResp.Body).Decode(&excludedRows); err != nil {
		return fmt.Errorf("decode excluded response: %w", err)
	}

	if len(excludedRows) > 0 {
		return nil // code is excluded, do not insert
	}

	body, err := json.Marshal(map[string]interface{}{
		"code":                 code,
		"policy_min_threshold": 0.05,
		"special_term":         true,
		"type":                 termType,
		"volatility":           volatility,
		"k_val":                k,
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
