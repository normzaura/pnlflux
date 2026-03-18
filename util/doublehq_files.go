package util

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// File maps to GET /api/clients/{clientId}/files (nested inside folders)
type File struct {
	FileID    int     `json:"fileId"`
	FileName  string  `json:"fileName"`
	FileType  string  `json:"fileType"`
	FileSize  int     `json:"fileSize"`
	CreatedAt *string `json:"createdAt"`
}

// FileFolder is the folder wrapper returned by /files
type FileFolder struct {
	FolderID   int    `json:"folderId"`
	FolderName string `json:"folderName"`
	Files      []File `json:"files"`
}

// ClientFiles holds the folder-grouped files returned by Double HQ.
// Note: the /attachments endpoint returns 500 for this practice — only /files is used.
type ClientFiles struct {
	Folders []FileFolder
}

// FetchClientFiles calls GET /api/clients/{clientId}/files and returns all folders and files.
func FetchClientFiles(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, clientID int) (*ClientFiles, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	folders, err := fetchFiles(ctx, httpClient, baseURL, token, clientID)
	if err != nil {
		return nil, fmt.Errorf("fetch files: %w", err)
	}

	return &ClientFiles{Folders: folders}, nil
}


func fetchFiles(ctx context.Context, httpClient *http.Client, baseURL, bearerToken string, clientID int) ([]FileFolder, error) {
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

	var result []FileFolder
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
