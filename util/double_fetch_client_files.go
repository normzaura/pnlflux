package util

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Attachment maps to GET /api/clients/{clientId}/attachments.
// Each attachment includes a pre-signed S3 downloadURL valid for 1 hour.
type Attachment struct {
	ID          int     `json:"id"`
	CreatedAt   *string `json:"createdAt"`
	Type        string  `json:"type"`
	FileName    string  `json:"fileName"`
	DownloadURL string  `json:"downloadURL"`
	Metadata    struct {
		IsVisible       bool    `json:"isVisible"`
		UpdatedFileName string  `json:"updatedFileName"`
		UploadedDate    *string `json:"uploadedDate"`
		TaskID          int     `json:"taskId"`
	} `json:"metadata"`
}

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

// ClientFiles holds attachments filtered to the triggering task, and the full file tree.
type ClientFiles struct {
	TaskAttachments []Attachment
	Root            *FileNode
}

// FetchClientFiles fetches all attachments for the client filtered to the given taskID,
// and the full file tree via GET /api/clients/{clientId}/files.
func FetchClientFiles(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, clientID, taskID int) (*ClientFiles, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	attachments, err := fetchAttachments(ctx, httpClient, baseURL, token, clientID, taskID)
	if err != nil {
		return nil, fmt.Errorf("fetch attachments: %w", err)
	}

	root, err := fetchFiles(ctx, httpClient, baseURL, token, clientID)
	if err != nil {
		return nil, fmt.Errorf("fetch files: %w", err)
	}

	return &ClientFiles{
		TaskAttachments: attachments,
		Root:            root,
	}, nil
}

func fetchAttachments(ctx context.Context, httpClient *http.Client, baseURL, bearerToken string, clientID, taskID int) ([]Attachment, error) {
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

	var all []Attachment
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}

	// Filter to attachments belonging to the triggering task.
	var filtered []Attachment
	for _, a := range all {
		if a.Metadata.TaskID == taskID {
			filtered = append(filtered, a)
		}
	}
	return filtered, nil
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
