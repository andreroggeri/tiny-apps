// Package crop asks tesourinha (apps/tesourinha) to cut a photographed page
// out of its background.
package crop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNoPage means tesourinha found no page to crop, or the photo is already
// cropped: keep the original.
var ErrNoPage = errors.New("no page to crop")

type Client struct {
	url  string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{url: strings.TrimRight(baseURL, "/") + "/crop", http: &http.Client{Timeout: 30 * time.Second}}
}

// Crop returns the page as a JPEG.
func (c *Client) Crop(ctx context.Context, image []byte, mimeType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(image))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mimeType)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusNoContent:
		return nil, ErrNoPage
	default:
		return nil, fmt.Errorf("tesourinha: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
}
