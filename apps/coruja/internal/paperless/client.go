// Package paperless is the slice of the paperless-ngx REST API coruja uses:
// read a document, wait for its AI suggestions, and write back a title, a
// custom field and shared tags.
package paperless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiVersion pins the response format (paperless 3).
const apiVersion = "10"

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// pollInterval is how often WaitForAI re-checks. Overridable in tests.
	pollInterval time.Duration
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		http:         &http.Client{Timeout: 60 * time.Second},
		pollInterval: 5 * time.Second,
	}
}

type FieldValue struct {
	Field int `json:"field"`
	Value any `json:"value"`
}

type Document struct {
	ID               int          `json:"id"`
	Title            string       `json:"title"`
	Content          string       `json:"content"`
	OriginalFileName string       `json:"original_file_name"`
	MimeType         string       `json:"mime_type"`
	Tags             []int        `json:"tags"`
	Correspondent    *int         `json:"correspondent"`
	DocumentType     *int         `json:"document_type"`
	CustomFields     []FieldValue `json:"custom_fields"`
}

// Field returns a custom field's value as text, or "".
func (d Document) Field(id int) string {
	for _, f := range d.CustomFields {
		if f.Field == id && f.Value != nil {
			return fmt.Sprint(f.Value)
		}
	}
	return ""
}

func (c *Client) Document(ctx context.Context, id int) (Document, error) {
	var d Document
	if err := c.do(ctx, http.MethodGet, "/api/documents/"+strconv.Itoa(id)+"/", nil, &d); err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return d, nil
}

// Original downloads the file as it was uploaded.
func (c *Client) Original(ctx context.Context, id int) ([]byte, error) {
	req, err := c.request(ctx, http.MethodGet, "/api/documents/"+strconv.Itoa(id)+"/download/?original=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 50<<20))
}

// WaitForAI waits until paperless' "apply AI suggestions" task for the
// document has finished, so its title and tags are in place. It returns
// right away when no such task shows up within grace: the document isn't
// handled by an AI workflow.
func (c *Client) WaitForAI(ctx context.Context, id int, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	for {
		var page struct {
			Results []struct {
				Status    string `json:"status"`
				InputData struct {
					DocumentID int `json:"document_id"`
				} `json:"input_data"`
			} `json:"results"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/tasks/?task_type=apply_ai_suggestions&page_size=25", nil, &page); err != nil {
			return fmt.Errorf("task list: %w", err)
		}
		found, running := false, false
		for _, t := range page.Results {
			if t.InputData.DocumentID != id {
				continue
			}
			found = true
			if s := strings.ToLower(t.Status); s == "pending" || s == "started" {
				running = true
			}
		}
		if found && !running || !found && time.Now().After(deadline) {
			return nil
		}
		t := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// CustomFieldID looks a custom field up by name.
func (c *Client) CustomFieldID(ctx context.Context, name string) (int, error) {
	var page struct {
		Results []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/custom_fields/?name__iexact="+url.QueryEscape(name), nil, &page); err != nil {
		return 0, fmt.Errorf("custom fields: %w", err)
	}
	for _, f := range page.Results {
		if strings.EqualFold(f.Name, name) {
			return f.ID, nil
		}
	}
	return 0, fmt.Errorf("custom field %q not found", name)
}

// Update patches a document. Note that custom_fields replaces the whole list.
func (c *Client) Update(ctx context.Context, id int, patch map[string]any) error {
	if err := c.do(ctx, http.MethodPatch, "/api/documents/"+strconv.Itoa(id)+"/", patch, nil); err != nil {
		return fmt.Errorf("update document: %w", err)
	}
	return nil
}

// Share clears the owner of a tag, correspondent or document type that has
// one, so every user sees it. kind is the API path: "tags",
// "correspondents" or "document_types". It reports whether it changed it.
func (c *Client) Share(ctx context.Context, kind string, id int) (bool, error) {
	path := "/api/" + kind + "/" + strconv.Itoa(id) + "/"
	var obj struct {
		Owner *int `json:"owner"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &obj); err != nil {
		return false, fmt.Errorf("get %s: %w", kind, err)
	}
	if obj.Owner == nil {
		return false, nil
	}
	if err := c.do(ctx, http.MethodPatch, path, map[string]any{"owner": nil}, nil); err != nil {
		return false, fmt.Errorf("share %s: %w", kind, err)
	}
	return true, nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json; version="+apiVersion)
	return req, nil
}

// do sends in (as JSON, if not nil) and decodes the response into out, if
// not nil.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
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
