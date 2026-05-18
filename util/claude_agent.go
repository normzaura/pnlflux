package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var jsonBlock = regexp.MustCompile(`\{[\s\S]*\}`)

// claudeHTTPClient is a dedicated client for Claude API calls with a longer
// timeout than the shared HttpClient used for Double HQ and Supabase requests.
var claudeHTTPClient = &http.Client{Timeout: 60 * time.Second}

// AgentAnalysisResult holds the two fields Claude returns for each analyzed row.
type AgentAnalysisResult struct {
	AgentKThreshold float64 `json:"agent_k_threshold"`
	Justification   string  `json:"justification"`
}

// agentPayload is the full context sent to Claude for a single account row.
type agentPayload struct {
	AccountCode   string               `json:"account_code"`
	AccountType   string               `json:"account_type"`
	Volatility    string               `json:"volatility"`
	ThresholdUsed float64              `json:"threshold_used"`
	KUsed         float64              `json:"k_used"`
	FlagRate               float64              `json:"flag_rate"`
	FluctuationStatus      string               `json:"fluctuation_status"`
	AvgAbsDelta            float64              `json:"avg_absdelta"`
	CoefficientOfVariation float64              `json:"coefficient_of_variation"`
	History                []map[string]float64 `json:"history"`
}

type claudeMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeReq struct {
	Model     string      `json:"model"`
	MaxTokens int         `json:"max_tokens"`
	System    string      `json:"system"`
	Messages  []claudeMsg `json:"messages"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeResp struct {
	Content []claudeContent `json:"content"`
}

// callClaudeAgent sends the row's analysis context to the Claude API and returns
// a recommended k threshold and justification for the account.
//
// Env vars required: CLAUDE_API_KEY, CLAUDE_MODEL, CLAUDE_BASE_URL.
// CLAUDE_SYSTEM is optional — omitting it sends no system prompt.
func callClaudeAgent(ctx context.Context, httpClient *http.Client, payload agentPayload) (AgentAnalysisResult, error) {
	apiKey := os.Getenv("CLAUDE_API_KEY")
	model := os.Getenv("CLAUDE_MODEL")
	baseURL := os.Getenv("CLAUDE_BASE_URL")
	system := os.Getenv("CLAUDE_SYSTEM")

	if apiKey == "" || model == "" || baseURL == "" {
		return AgentAnalysisResult{}, fmt.Errorf("CLAUDE_API_KEY, CLAUDE_MODEL, and CLAUDE_BASE_URL must be set")
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return AgentAnalysisResult{}, fmt.Errorf("marshal agent payload: %w", err)
	}

	body, err := json.Marshal(claudeReq{
		Model:     model,
		MaxTokens: 1024,
		System:    system,
		Messages:  []claudeMsg{{Role: "user", Content: string(payloadJSON)}},
	})
	if err != nil {
		return AgentAnalysisResult{}, fmt.Errorf("marshal claude request: %w", err)
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return AgentAnalysisResult{}, fmt.Errorf("build claude request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := claudeHTTPClient.Do(req)
	if err != nil {
		return AgentAnalysisResult{}, fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return AgentAnalysisResult{}, fmt.Errorf("claude returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var cr claudeResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return AgentAnalysisResult{}, fmt.Errorf("decode claude response: %w", err)
	}
	if len(cr.Content) == 0 {
		return AgentAnalysisResult{}, fmt.Errorf("claude returned empty content")
	}

	rawText := cr.Content[0].Text
	fmt.Printf("[claude-agent] raw response: %s\n", rawText)

	jsonStr := jsonBlock.FindString(rawText)
	if jsonStr == "" {
		return AgentAnalysisResult{}, fmt.Errorf("no JSON object found in claude response")
	}

	var result AgentAnalysisResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return AgentAnalysisResult{}, fmt.Errorf("parse claude result JSON: %w", err)
	}
	return result, nil
}
