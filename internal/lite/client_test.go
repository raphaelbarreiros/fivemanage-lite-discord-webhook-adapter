package lite_test

import (
	"context"
	"local/fivemanage-discord-adapter/internal/event"
	"local/fivemanage-discord-adapter/internal/lite"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOnlyConfirmedWriteIsSuccess(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		code                 int
		header, body         string
		confirmed, permanent bool
	}{
		{"original false positive", 200, "", `{"status":"ok","data":"ok"}`, false, false},
		{"malformed confirmation", 200, lite.Confirmation, `{"status":"ok","data":"not-ok"}`, false, false},
		{"confirmed", 200, lite.Confirmation, `{"status":"ok","data":"ok"}`, true, false},
		{"storage failure", 503, "", "", false, false}, {"dataset missing", 400, "", "", false, true}, {"rate limit", 429, "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Fivemanage-Ingest", tc.header)
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer s.Close()
			c := lite.New(s.URL, time.Second)
			defer c.Close()
			result := c.Send(context.Background(), "synthetic-token", "synthetic", event.Log{Message: "synthetic"})
			if result.Confirmed != tc.confirmed || result.Permanent != tc.permanent {
				t.Fatal(result)
			}
		})
	}
}
func TestRedirectNeverLeaksCredentials(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c := lite.New(source.URL, time.Second)
	defer c.Close()
	r := c.Send(context.Background(), "synthetic-token", "synthetic", event.Log{Message: "synthetic"})
	if r.Confirmed || hits.Load() != 0 {
		t.Fatal("followed credential-bearing redirect")
	}
}
