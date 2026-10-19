// Package bitbucket is a minimal Bitbucket Data Center REST client.
package bitbucket

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// UserSlug caches the authenticated user's slug.
	UserSlug string
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

type APIError struct {
	StatusCode int
	Method     string
	URL        string
	Messages   []string
}

func (e *APIError) Error() string {
	msg := strings.Join(e.Messages, "; ")
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s (%s %s)", e.StatusCode, msg, e.Method, e.URL)
}

// URL builds an absolute URL. Paths are relative to the base URL.
func (c *Client) URL(path string, query url.Values) string {
	u := c.BaseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + query.Encode()
	}
	return u
}

// Raw performs a request and returns the response; non-2xx statuses are
// returned as *APIError (with the body consumed).
func (c *Client) Raw(method, path string, query url.Values, headers http.Header, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.URL(path, query), body)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// Required by Bitbucket's XSRF protection for non-GET API calls.
	req.Header.Set("X-Atlassian-Token", "no-check")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return resp, parseError(resp)
	}
	return resp, nil
}

func parseError(resp *http.Response) error {
	e := &APIError{StatusCode: resp.StatusCode, Method: resp.Request.Method, URL: resp.Request.URL.String()}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &payload) == nil {
		for _, m := range payload.Errors {
			e.Messages = append(e.Messages, m.Message)
		}
	}
	if len(e.Messages) == 0 && len(data) > 0 && len(data) < 500 && !bytes.Contains(data, []byte("<html")) {
		e.Messages = []string{strings.TrimSpace(string(data))}
	}
	return e
}

// Do sends body (JSON-encoded if non-nil) and decodes the response into out (if non-nil).
func (c *Client) Do(method, path string, query url.Values, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	resp, err := c.Raw(method, path, query, nil, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type page[T any] struct {
	Values        []T  `json:"values"`
	IsLastPage    bool `json:"isLastPage"`
	NextPageStart int  `json:"nextPageStart"`
}

// Paginate collects up to max items (max <= 0: all) accepted by keep (nil: all).
func Paginate[T any](c *Client, path string, query url.Values, max int, keep func(T) bool) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	pageSize := 100
	if max > 0 && max < pageSize && keep == nil {
		pageSize = max
	}
	q.Set("limit", strconv.Itoa(pageSize))
	var out []T
	start := 0
	for {
		q.Set("start", strconv.Itoa(start))
		var p page[T]
		if err := c.Do("GET", path, q, nil, &p); err != nil {
			return nil, err
		}
		for _, v := range p.Values {
			if keep != nil && !keep(v) {
				continue
			}
			out = append(out, v)
			if max > 0 && len(out) >= max {
				return out, nil
			}
		}
		if p.IsLastPage || len(p.Values) == 0 {
			return out, nil
		}
		start = p.NextPageStart
	}
}

// CurrentUser returns the authenticated user's slug.
func (c *Client) CurrentUser() (string, error) {
	if c.UserSlug != "" {
		return c.UserSlug, nil
	}
	u, err := c.Whoami()
	if err != nil {
		return "", err
	}
	c.UserSlug = u.Slug
	return u.Slug, nil
}

// Whoami resolves the authenticated user via the applinks whoami servlet,
// falling back to the X-AUSERNAME header.
func (c *Client) Whoami() (*User, error) {
	var name string
	resp, err := c.Raw("GET", "plugins/servlet/applinks/whoami", nil, http.Header{"Accept": {"text/plain"}}, nil)
	if err == nil {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		name = strings.TrimSpace(string(data))
	}
	if name == "" {
		resp, err := c.Raw("GET", "rest/api/1.0/application-properties", nil, nil, nil)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		name = resp.Header.Get("X-AUSERNAME")
	}
	if name == "" {
		return nil, &APIError{StatusCode: 401, Method: "GET", URL: c.BaseURL, Messages: []string{"token not accepted (anonymous access)"}}
	}
	return c.ResolveUser(name)
}
