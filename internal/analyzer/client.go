// Package analyzer talks to the Python pixel stage (analyzer/ in this repo): a loopback HTTP service that decodes
// images, runs pose detection and measures focus. In the cluster it runs as a sidecar in the API pod; locally, Spawn
// starts it as a child process.
package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mononendev/photosort/internal/pj"
)

// ErrGone means a measurement token expired before finalize (e.g. the analyzer restarted): measure again.
var ErrGone = errors.New("analyzer: measurement expired")

// Client calls one analyzer.
type Client struct {
	Base string
	HTTP *http.Client
}

// New is a client for the analyzer at base (e.g. http://127.0.0.1:8090).
func New(base string) *Client {
	return &Client{Base: base, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Error is a non-2xx answer.
type Error struct {
	Status int
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("analyzer: %d %s", e.Status, e.Detail) }

func (c *Client) post(ctx context.Context, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("analyzer unreachable at %s: %w", c.Base, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusGone {
		return nil, ErrGone
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Detail any }
		if json.Unmarshal(out, &e) == nil && e.Detail != nil {
			return nil, &Error{resp.StatusCode, fmt.Sprint(e.Detail)}
		}
		return nil, &Error{resp.StatusCode, string(out)}
	}
	return out, nil
}

func (c *Client) postObj(ctx context.Context, path string, body any) (pj.Obj, error) {
	b, err := c.post(ctx, path, body)
	if err != nil {
		return nil, err
	}
	var o pj.Obj
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("analyzer %s: %w", path, err)
	}
	return o, nil
}

// Measure runs the first half of the local stage on one image (see analyzer/photosort_analyzer/measure.py).
func (c *Client) Measure(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	return c.postObj(ctx, "/measure", req)
}

// Finalize runs the second half, given the people order the rules picked.
func (c *Client) Finalize(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	return c.postObj(ctx, "/finalize", req)
}

// Detect runs one pose model on one image without storing anything (the UI's detector comparison).
func (c *Client) Detect(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	return c.postObj(ctx, "/detect", req)
}

// FocusDebug recomputes the focus math's visual intermediates for the detail view.
func (c *Client) FocusDebug(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	return c.postObj(ctx, "/focus-debug", req)
}

// RenderFull is the full-resolution JPEG of an image, decoded and lifted as the metrics saw it.
func (c *Client) RenderFull(ctx context.Context, path string, exposure any, quality int) ([]byte, error) {
	return c.post(ctx, "/render-full", pj.Obj{"path": path, "exposure": exposure, "quality": quality})
}

// Health is the analyzer's /health answer.
func (c *Client) Health(ctx context.Context) (pj.Obj, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var o pj.Obj
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, err
	}
	return o, nil
}
