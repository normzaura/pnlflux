package util

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// FileNode represents a file or folder in Double HQ's file tree.
// The root node returned by /files contains Children which may be files or subfolders.
type FileNode struct {
	ID                      int        `json:"id"`
	Name                    string     `json:"name"`
	Type                    string     `json:"type"` // "folder" or "file"
	CreatedAt               *string    `json:"createdAt"`
	S3Key                   *string    `json:"s3Key"`
	IsVisibleInClientPortal bool       `json:"isVisibleInClientPortal"`
	ClientDescription       *string    `json:"clientDescription"`
	DeletedAt               *string    `json:"deletedAt"`
	ParentID                *int       `json:"parentId"`
	CreatorID               *int       `json:"creatorId"`
	ClientID                int        `json:"clientId"`
	Children                []FileNode `json:"children"`
}

// ClientFiles holds the file tree returned by Double HQ.
type ClientFiles struct {
	Root *FileNode
}

// FetchClientFiles calls GET /api/clients/{clientId}/files and returns all folders and files.
func FetchClientFiles(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, clientID int) (*ClientFiles, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	root, err := fetchFiles(ctx, httpClient, baseURL, token, clientID)
	if err != nil {
		return nil, fmt.Errorf("fetch files: %w", err)
	}

	return &ClientFiles{Root: root}, nil
}

func fetchFiles(ctx context.Context, httpClient *http.Client, baseURL, bearerToken string, clientID int) (*FileNode, error) {
	url := fmt.Sprintf("%s/api/clients/%d/files", baseURL, clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result FileNode
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
