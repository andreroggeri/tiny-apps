// Package paperless is a minimal client for the paperless-ngx REST API:
// upload a document, wait for it to be consumed, and read it back.
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

type Document struct {
	ID            int
	Title         string
	Tags          []string
	Correspondent string
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

// WaitForTitleChange polls the document until its title differs from initial
// (i.e. the AI workflow has renamed it) or ctx expires, and returns the latest
// state either way. A ctx deadline is not an error here.
func (c *Client) WaitForTitleChange(ctx context.Context, id int, initial string) (Document, error) {
	var doc Document
	var err error
	for {
		doc, err = c.Document(context.WithoutCancel(ctx), id)
		if err != nil || doc.Title != initial {
			return doc, err
		}
		if c.sleep(ctx) != nil {
			return doc, nil
		}
	}
}

// Document fetches a document with tag and correspondent names resolved.
func (c *Client) Document(ctx context.Context, id int) (Document, error) {
	var raw struct {
		ID            int    `json:"id"`
		Title         string `json:"title"`
		Tags          []int  `json:"tags"`
		Correspondent *int   `json:"correspondent"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/documents/"+strconv.Itoa(id)+"/", nil, "", &raw); err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	doc := Document{ID: raw.ID, Title: raw.Title}

	if len(raw.Tags) > 0 {
		ids := make([]string, len(raw.Tags))
		for i, t := range raw.Tags {
			ids[i] = strconv.Itoa(t)
		}
		var tags struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		path := "/api/tags/?page_size=100&id__in=" + strings.Join(ids, ",")
		if err := c.do(ctx, http.MethodGet, path, nil, "", &tags); err != nil {
			return Document{}, fmt.Errorf("get tags: %w", err)
		}
		for _, t := range tags.Results {
			doc.Tags = append(doc.Tags, t.Name)
		}
	}

	if raw.Correspondent != nil {
		var corr struct {
			Name string `json:"name"`
		}
		path := "/api/correspondents/" + strconv.Itoa(*raw.Correspondent) + "/"
		if err := c.do(ctx, http.MethodGet, path, nil, "", &corr); err != nil {
			return Document{}, fmt.Errorf("get correspondent: %w", err)
		}
		doc.Correspondent = corr.Name
	}
	return doc, nil
}

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
