// Package paperless is a minimal client for the paperless-ngx REST API:
// upload a document, wait for it to be consumed, read its text, and delete it.
package paperless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiVersion pins the response format. v10 (paperless 3) paginates the task
// list and reports related_document_ids.
const apiVersion = "10"

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// pollInterval is how often Wait* methods re-check. Overridable in tests.
	pollInterval time.Duration
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		http:         &http.Client{Timeout: 60 * time.Second},
		pollInterval: 3 * time.Second,
	}
}

var ErrConsumeFailed = errors.New("paperless failed to consume the document")

// Upload sends a file for consumption and returns the task ID.
func (c *Client) Upload(ctx context.Context, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("document", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	var taskID string
	err = c.do(ctx, http.MethodPost, "/api/documents/post_document/", &body, w.FormDataContentType(), &taskID)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	return taskID, nil
}

// WaitForDocument polls the consumption task until it finishes and returns the
// new document's ID.
func (c *Client) WaitForDocument(ctx context.Context, taskID string) (int, error) {
	for {
		var page struct {
			Results []struct {
				Status             string          `json:"status"`
				RelatedDocumentIDs []int           `json:"related_document_ids"`
				ResultData         json.RawMessage `json:"result_data"`
			} `json:"results"`
		}
		path := "/api/tasks/?task_id=" + url.QueryEscape(taskID)
		if err := c.do(ctx, http.MethodGet, path, nil, "", &page); err != nil {
			return 0, fmt.Errorf("task status: %w", err)
		}
		if len(page.Results) > 0 {
			t := page.Results[0]
			switch strings.ToLower(t.Status) {
			case "success":
				if len(t.RelatedDocumentIDs) == 0 {
					// Duplicate or otherwise no document created.
					return 0, fmt.Errorf("%w: %s", ErrConsumeFailed, string(t.ResultData))
				}
				return t.RelatedDocumentIDs[0], nil
			case "failure", "revoked":
				return 0, fmt.Errorf("%w: %s", ErrConsumeFailed, string(t.ResultData))
			}
		}
		if err := c.sleep(ctx); err != nil {
			return 0, err
		}
	}
}

// AddNote attaches a note to a document.
func (c *Client) AddNote(ctx context.Context, id int, note string) error {
	body, err := json.Marshal(map[string]string{"note": note})
	if err != nil {
		return err
	}
	var ignored json.RawMessage
	path := "/api/documents/" + strconv.Itoa(id) + "/notes/"
	if err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json", &ignored); err != nil {
		return fmt.Errorf("add note: %w", err)
	}
	return nil
}

// Content returns a document's OCR text.
func (c *Client) Content(ctx context.Context, id int) (string, error) {
	var doc struct {
		Content string `json:"content"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/documents/"+strconv.Itoa(id)+"/", nil, "", &doc); err != nil {
		return "", fmt.Errorf("get document: %w", err)
	}
	return doc.Content, nil
}

// Delete moves a document to paperless' trash.
func (c *Client) Delete(ctx context.Context, id int) error {
	if err := c.do(ctx, http.MethodDelete, "/api/documents/"+strconv.Itoa(id)+"/", nil, "", nil); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// do sends a request and decodes the JSON response into out, if not nil.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json; version="+apiVersion)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) sleep(ctx context.Context) error {
	t := time.NewTimer(c.pollInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
