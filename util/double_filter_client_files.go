package util

import (
	"bytes"
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


// ClientFiles holds attachments filtered to the triggering task.
type ClientFiles struct {
	TaskAttachments []Attachment
}

// FilterClientAttachedFiles fetches all attachments for the client filtered to the given taskID.
func FilterClientAttachedFiles(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, clientID, taskID int) (*ClientFiles, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	attachments, err := fetchAttachments(ctx, httpClient, baseURL, token, clientID, taskID)
	if err != nil {
		return nil, fmt.Errorf("fetch attachments: %w", err)
	}

	return &ClientFiles{TaskAttachments: attachments}, nil
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

// PatchTaskSubText appends text to a task's subText field via PATCH /api/tasks/{taskId}.
func PatchTaskSubText(ctx context.Context, httpClient *http.Client, baseURL string, tokens *TokenProvider, taskID int, subText string) error {
	token, err := tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	body, err := json.Marshal(map[string]string{"subText": subText})
	if err != nil {
		return fmt.Errorf("marshal patch body: %w", err)
	}

	url := fmt.Sprintf("%s/api/tasks/%d", baseURL, taskID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build patch request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch task: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("patch task returned %d", resp.StatusCode)
	}
	return nil
}
