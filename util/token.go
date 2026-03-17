package util

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenProvider fetches and caches a Double HQ OAuth2 client credentials token.
// It is safe for concurrent use.
type TokenProvider struct {
	mu           sync.Mutex
	clientID     string
	clientSecret string
	tokenURL     string
	httpClient   *http.Client

	cachedToken string
	expiresAt   time.Time
}

// NewTokenProvider constructs a TokenProvider.
// tokenURL is the Double HQ OAuth2 token endpoint.
func NewTokenProvider(httpClient *http.Client, tokenURL, clientID, clientSecret string) *TokenProvider {
	return &TokenProvider{
		httpClient:   httpClient,
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// Token returns a valid bearer token, fetching a new one if the cached
// token is missing or within 30 seconds of expiry.
func (p *TokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cachedToken != "" && time.Now().Before(p.expiresAt.Add(-30*time.Second)) {
		return p.cachedToken, nil
	}

	return p.fetch(ctx)
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// fetch performs the client credentials grant. Must be called with mu held.
func (p *TokenProvider) fetch(ctx context.Context) (string, error) {
	body := url.Values{}
	body.Set("grant_type", "client_credentials")
	body.Set("client_id", p.clientID)
	body.Set("client_secret", p.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}

	var t tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	p.cachedToken = t.AccessToken
	p.expiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)

	return p.cachedToken, nil
}
