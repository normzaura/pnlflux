package util

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type DoubleClient struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type EndCloseSummary struct {
	ID         int     `json:"id"`
	Status     string  `json:"status"`
	YearMonth  string  `json:"yearMonth"`
	Progress   string  `json:"progress"`
	DueDate    *string `json:"dueDate"`
	ClientID   int     `json:"clientId"`
	ClientName string  `json:"clientName"`
}

// FetchAllClients pages through GET /api/clients (max 100 per page) and returns all clients.
func FetchAllClients(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider) ([]DoubleClient, error) {
	const pageSize = 100
	var all []DoubleClient
	offset := 0

	for {
		token, err := tokens.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("get token: %w", err)
		}

		reqURL := fmt.Sprintf("%s/api/clients?limit=%d&offset=%d", baseURL, pageSize, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build clients request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch clients: %w", err)
		}

		var page []DoubleClient
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch clients returned %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode clients response: %w", decodeErr)
		}

		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		offset += pageSize
	}
	return all, nil
}

// FetchClientEndCloses calls GET /api/clients/{clientId}/end-closes and returns all periods.
// Returns nil, nil if the client has no end closes (404).
func FetchClientEndCloses(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, clientID int) ([]EndCloseSummary, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	reqURL := fmt.Sprintf("%s/api/clients/%d/end-closes", baseURL, clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build end-closes request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch end-closes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch end-closes for client %d returned %d", clientID, resp.StatusCode)
	}

	var closes []EndCloseSummary
	if err := json.NewDecoder(resp.Body).Decode(&closes); err != nil {
		return nil, fmt.Errorf("decode end-closes response: %w", err)
	}
	return closes, nil
}
