package util

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Attachment maps to GET /api/clients/{clientId}/attachments
type Attachment struct {
	ID        int     `json:"id"`
	CreatedAt *string `json:"createdAt"`
	Type      string  `json:"type"`
	FileName  string  `json:"fileName"`
	Metadata  struct {
		IsVisible       bool    `json:"isVisible"`
		UpdatedFileName string  `json:"updatedFileName"`
		UploadedDate    *string `json:"uploadedDate"`
	} `json:"metadata"`
}

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

// ClientFiles is the combined result of both Double HQ file endpoints.
type ClientFiles struct {
	Attachments []Attachment
	Folders     []FileFolder
}

// FetchClientFiles calls both Double HQ file endpoints for the given clientId
// and returns all attachments and folder-grouped files.
func FetchClientFiles(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, clientID int) (*ClientFiles, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	attachments, err := fetchAttachments(ctx, httpClient, baseURL, token, clientID)
	if err != nil {
		return nil, fmt.Errorf("fetch attachments: %w", err)
	}

	folders, err := fetchFiles(ctx, httpClient, baseURL, token, clientID)
	if err != nil {
		return nil, fmt.Errorf("fetch files: %w", err)
	}

	return &ClientFiles{
		Attachments: attachments,
		Folders:     folders,
	}, nil
}

func fetchAttachments(ctx context.Context, httpClient *http.Client, baseURL, bearerToken string, clientID int) ([]Attachment, error) {
	url := fmt.Sprintf("%s/api/clients/%d/attachments", baseURL, clientID)

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

	var result []Attachment
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
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
