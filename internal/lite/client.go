package lite

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"local/fivemanage-discord-adapter/internal/event"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const Confirmation = "clickhouse-sync-v1"

type Result struct {
	Confirmed, Permanent bool
	Status               int
	RetryAfter           time.Duration
	Reason               string
}
type Client struct {
	endpoint string
	http     *http.Client
}

func New(base string, timeout time.Duration) *Client {
	return &Client{endpoint: strings.TrimRight(base, "/") + "/api/logs", http: &http.Client{Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxConnsPerHost: 1, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: timeout, DisableCompression: true}}}
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) Send(ctx context.Context, token, dataset string, log event.Log) Result {
	body, err := json.Marshal([]event.Log{log})
	if err != nil {
		return Result{Permanent: true, Reason: "encoding"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Reason: "configuration"}
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("X-Fivemanage-Dataset", dataset)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Reason: "transport"}
	}
	defer resp.Body.Close()
	r := Result{Status: resp.StatusCode, Reason: "remote_status"}
	if sec, e := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 32); e == nil && sec > 0 {
		r.RetryAfter = time.Duration(sec) * time.Second
	} else if when, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
		r.RetryAfter = max(0, time.Until(when))
	}
	switch resp.StatusCode {
	case 400, 404, 405, 413, 415, 422:
		r.Permanent = true
	}
	// Original Lite can return 200 on failed writes. Never accept it without this patch contract.
	if resp.StatusCode != http.StatusOK {
		return r
	}
	if resp.Header.Get("X-Fivemanage-Ingest") != Confirmation {
		r.Reason = "unconfirmed_contract"
		return r
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(b) > 4096 {
		r.Reason = "invalid_confirmation"
		return r
	}
	var ack struct {
		Status string `json:"status"`
		Data   string `json:"data"`
	}
	if json.Unmarshal(b, &ack) != nil || ack.Status != "ok" || ack.Data != "ok" {
		r.Reason = "invalid_confirmation"
		return r
	}
	r.Confirmed = true
	r.Reason = "confirmed"
	return r
}
