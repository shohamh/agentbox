// Package ch is a minimal ClickHouse HTTP client returning JSONEachRow rows.
package ch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	URL, User, Password, Database string
	HTTP                          *http.Client
}

func New(u, user, pw, database string) *Client {
	return &Client{URL: strings.TrimRight(u, "/"), User: user, Password: pw, Database: database, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Query runs q (without FORMAT) with named parameters {name:Type} and
// returns rows as maps.
func (c *Client) Query(ctx context.Context, q string, params map[string]string) ([]map[string]any, error) {
	v := url.Values{}
	v.Set("database", c.Database)
	for k, p := range params {
		v.Set("param_"+k, p)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.URL+"/?"+v.Encode(), bytes.NewBufferString(q+" FORMAT JSONEachRow"))
	if c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("clickhouse: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err == nil {
			out = append(out, m)
		}
	}
	return out, sc.Err()
}

func Str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func MapStr(m map[string]any, k, key string) string {
	if mm, ok := m[k].(map[string]any); ok {
		return Str(mm, key)
	}
	return ""
}
