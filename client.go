package ncbigenome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the NCBI Datasets v2 API root.
const DefaultBaseURL = "https://api.ncbi.nlm.nih.gov/datasets/v2"

// Options configures a Client. The zero value is usable.
type Options struct {
	// BaseURL overrides DefaultBaseURL (e.g. for a gateway or a test server).
	BaseURL string
	// APIKey is an NCBI API key, sent as the "api-key" header. NCBI documents a
	// default limit of 5 requests/second, raised to 10 with a key.
	APIKey string
	// HTTPClient is used for all requests. Do not set a Timeout on it: package
	// downloads can be long; use the context for deadlines instead.
	HTTPClient *http.Client
	// UserAgent overrides the default User-Agent.
	UserAgent string
	// MaxRetries is the number of retries after the first attempt.
	// 0 means the default (10); a negative value disables retries.
	MaxRetries int
	// RetryWaitMin and RetryWaitMax bound the exponential backoff
	// (defaults 1s and 30s, as in the CLI).
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration
	// Headers are extra request headers.
	Headers map[string]string
	// OnMessage receives server notices from the X-Datasets-Version-Message and
	// X-Datasets-User-Message response headers (each distinct text once).
	OnMessage func(msg string)
	// TempDir is where Download keeps its temporary copy when Request.Manifest
	// is set. The default is the system temp directory.
	TempDir string
}

// Client talks to the NCBI Datasets API. It is safe for concurrent use.
type Client struct {
	baseURL    string
	apiKey     string
	hc         *http.Client
	userAgent  string
	maxRetries int
	waitMin    time.Duration
	waitMax    time.Duration
	headers    map[string]string
	onMessage  func(string)
	tempDir    string

	mu   sync.Mutex
	seen map[string]bool
}

// New returns a Client.
func New(o Options) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(o.BaseURL, "/"),
		apiKey:     o.APIKey,
		hc:         o.HTTPClient,
		userAgent:  o.UserAgent,
		maxRetries: o.MaxRetries,
		waitMin:    o.RetryWaitMin,
		waitMax:    o.RetryWaitMax,
		onMessage:  o.OnMessage,
		tempDir:    o.TempDir,
		seen:       map[string]bool{},
		headers:    map[string]string{},
	}
	for k, v := range o.Headers {
		c.headers[k] = v
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.hc == nil {
		c.hc = &http.Client{}
	}
	if c.userAgent == "" {
		c.userAgent = "ncbigenome-go"
	}
	if c.maxRetries == 0 {
		c.maxRetries = 10
	} else if c.maxRetries < 0 {
		c.maxRetries = 0
	}
	if c.waitMin <= 0 {
		c.waitMin = time.Second
	}
	if c.waitMax <= 0 {
		c.waitMax = 30 * time.Second
	}
	return c
}

// APIError is returned when the API answers with an error status.
type APIError struct {
	StatusCode int
	Status     string // e.g. "404 Not Found"
	Message    string // message from the response body, if any
	Hint       string // advice for well-known cases
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ncbi datasets API: HTTP %s", e.Status)
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.Hint != "" {
		b.WriteString(" (" + e.Hint + ")")
	}
	return b.String()
}

// apiError builds an APIError from a non-success response, reading at most
// 64 KiB of the body (the API's error body is a small {"message": ...} object
// or plain text).
func apiError(resp *http.Response, hint string) *APIError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := strings.TrimSpace(string(data))
	var st struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &st) == nil && st.Message != "" {
		msg = st.Message
	}
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	return &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Message: msg, Hint: hint}
}

// post sends a JSON POST, retrying like the CLI does: transport errors, 429 and
// 5xx other than 501. The final response is returned as-is when retries run out,
// so callers can map its status. The caller closes resp.Body.
func (c *Client) post(ctx context.Context, path string, body any, accept string) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", c.userAgent)
		if c.apiKey != "" {
			req.Header.Set("api-key", c.apiKey)
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}

		resp, err := c.hc.Do(req)
		if !c.retryable(ctx, resp, err) || attempt >= c.maxRetries {
			if err != nil {
				if attempt > 0 {
					err = fmt.Errorf("giving up after %d attempts: %w", attempt+1, err)
				}
				return nil, err
			}
			c.notify(resp)
			return resp, nil
		}
		wait := c.backoff(attempt, resp)
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) retryable(ctx context.Context, resp *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}
	return resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented)
}

func (c *Client) backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
		if s := resp.Header.Get("Retry-After"); s != "" {
			if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
				d := time.Duration(secs) * time.Second
				if d > 5*time.Minute {
					d = 5 * time.Minute
				}
				return d
			}
		}
	}
	d := c.waitMin
	for i := 0; i < attempt && d < c.waitMax; i++ {
		d *= 2
	}
	if d > c.waitMax {
		d = c.waitMax
	}
	return d
}

func (c *Client) notify(resp *http.Response) {
	if c.onMessage == nil || resp == nil {
		return
	}
	for _, h := range []string{"X-Datasets-Version-Message", "X-Datasets-User-Message"} {
		v := resp.Header.Get(h)
		if v == "" {
			continue
		}
		c.mu.Lock()
		dup := c.seen[v]
		c.seen[v] = true
		c.mu.Unlock()
		if !dup {
			c.onMessage(v)
		}
	}
}
