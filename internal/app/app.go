package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"local/fivemanage-discord-adapter/internal/config"
	"local/fivemanage-discord-adapter/internal/event"
	"local/fivemanage-discord-adapter/internal/lite"
	"local/fivemanage-discord-adapter/internal/queue"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type App struct {
	Config         config.Config
	Queue          *queue.Queue
	Metrics        Metrics
	logger         *slog.Logger
	client         *lite.Client
	sources        map[string]config.Source
	tokens         map[string][32]byte
	ingress, admin chan struct{}
	stopped        atomic.Bool
}

func New(c config.Config, q *queue.Queue, logger *slog.Logger) *App {
	a := &App{Config: c, Queue: q, logger: logger, client: lite.New(c.LiteURL, c.HTTPTimeout.Value()), sources: map[string]config.Source{}, tokens: map[string][32]byte{}, ingress: make(chan struct{}, c.MaxInflight), admin: make(chan struct{}, 2)}
	for _, s := range c.Sources {
		a.sources[s.ID] = s
		a.tokens[s.ID] = sha256.Sum256([]byte(s.Secret))
	}
	return a
}
func (a *App) StopAdmissions()       { a.stopped.Store(true) }
func (a *App) Close()                { a.client.Close() }
func (a *App) Handler() http.Handler { return http.HandlerFunc(a.receive) }
func (a *App) receive(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	a.Metrics.Received.Add(1)
	defer func() { a.Metrics.AdmissionNS.Add(uint64(time.Since(start))) }()
	reject := func(status int, msg string) { a.Metrics.Rejected.Add(1); http.Error(w, msg, status) }
	// Deliberately no ServeMux redirects, access logger, URL or payload in diagnostics.
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 5 || parts[1] != "api" || parts[2] != "webhooks" {
		reject(404, "not found")
		return
	}
	want, known := a.tokens[parts[3]]
	got := sha256.Sum256([]byte(parts[4]))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 || !known {
		reject(401, "invalid credentials")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		reject(405, "only POST supported")
		return
	}
	if a.stopped.Load() {
		reject(503, "shutting down")
		return
	}
	select {
	case a.ingress <- struct{}{}:
		defer func() { <-a.ingress }()
	default:
		w.Header().Set("Retry-After", "1")
		reject(503, "admission limit")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		reject(400, "invalid query")
		return
	}
	for key, values := range query {
		if key != "wait" || len(values) != 1 || (values[0] != "false" && values[0] != "0") {
			reject(400, "unsupported webhook query; only wait=false")
			return
		}
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		reject(415, "only application/json supported")
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		reject(415, "content encoding unsupported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.Config.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			reject(413, "body too large")
		} else {
			reject(400, "cannot read body")
		}
		return
	}
	if _, err = event.Decode(raw); err != nil {
		if errors.Is(err, event.ErrUnsupported) {
			reject(400, "unsupported Discord feature")
		} else {
			reject(400, "invalid webhook JSON")
		}
		return
	}
	var idBytes [16]byte
	if _, err = rand.Read(idBytes[:]); err != nil {
		reject(503, "identifier unavailable")
		return
	}
	e := queue.Entry{ID: hex.EncodeToString(idBytes[:]), Source: parts[3], Payload: raw, Received: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(r.Context(), a.Config.AdmissionTimeout.Value())
	defer cancel()
	removed, err := a.Queue.Enqueue(ctx, e)
	if err != nil {
		status := 503
		if errors.Is(err, queue.ErrFull) {
			status = 429
		}
		if errors.Is(err, queue.ErrDisk) {
			status = 507
		}
		a.Metrics.LocalErrors.Add(1)
		w.Header().Set("Retry-After", "1")
		reject(status, "queue unavailable")
		return
	}
	a.discards(removed.Expired, removed.Overflow)
	a.Metrics.Accepted.Add(1)
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "only GET", 405)
			return
		}
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path != "/readyz" && r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		select {
		case a.admin <- struct{}{}:
			defer func() { <-a.admin }()
		default:
			http.Error(w, "busy", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if r.URL.Path == "/readyz" {
			if a.stopped.Load() || a.Queue.Ready(ctx) != nil {
				http.Error(w, "not ready", 503)
			} else {
				w.WriteHeader(200)
			}
			return
		}
		s, err := a.Queue.Stats(ctx)
		if err != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		a.Metrics.write(w, s)
	})
}
func (a *App) discards(expired, overflow int64) {
	if expired > 0 {
		a.Metrics.Expired.Add(uint64(expired))
		a.logger.Warn("pending discarded", "reason", "expired", "count", expired)
	}
	if overflow > 0 {
		a.Metrics.Overflow.Add(uint64(overflow))
		a.logger.Warn("pending discarded", "reason", "overflow", "count", overflow)
	}
}
func (a *App) backoff(attempt int) time.Duration {
	d := a.Config.RetryMin.Value()
	for i := 1; i < attempt && d < a.Config.RetryMax.Value(); i++ {
		if d > a.Config.RetryMax.Value()/2 {
			return a.Config.RetryMax.Value()
		}
		d *= 2
	}
	return min(d, a.Config.RetryMax.Value())
}
func (a *App) RunWorker(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	maintenance := time.NewTicker(a.Config.Queue.MaintenanceInterval.Value())
	defer maintenance.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-maintenance.C:
			n, err := a.Queue.Expire(ctx, time.Now())
			if err != nil {
				a.localError("expire")
			} else {
				a.discards(n, 0)
			}
			busy, err := a.Queue.Maintain(ctx)
			if err != nil {
				a.localError("maintenance")
			}
			if busy {
				a.Metrics.CheckpointBusy.Add(1)
			}
		case <-timer.C:
			delay := a.step(ctx)
			timer.Reset(max(a.Config.SendInterval.Value(), delay))
		}
	}
}
func (a *App) localError(op string) {
	a.Metrics.LocalErrors.Add(1)
	a.logger.Warn("local operation failed", "operation", op)
}
func (a *App) step(ctx context.Context) time.Duration {
	e, err := a.Queue.Reserve(ctx, time.Now(), func(attempt int) time.Duration { return a.Config.HTTPTimeout.Value() + a.backoff(attempt) })
	if err != nil {
		if ctx.Err() == nil {
			a.localError("reserve")
		}
		return a.Config.RetryMin.Value()
	}
	if e == nil {
		return a.Config.SendInterval.Value()
	}
	defer a.Queue.Release(e.ID)
	source, ok := a.sources[e.Source]
	discard := func(reason string) {
		if err := a.Queue.Delete(ctx, e.ID); err != nil {
			a.localError("discard")
			return
		}
		if reason == "attempts" {
			a.Metrics.Exhausted.Add(1)
		} else {
			a.Metrics.Permanent.Add(1)
		}
		a.logger.Warn("pending discarded", "reason", reason, "event_id", e.ID)
	}
	if e.Attempts > a.Config.MaxAttempts {
		discard("attempts")
		return 0
	}
	if !ok {
		discard("source_removed")
		return 0
	}
	if e.Attempts > 1 {
		a.Metrics.Retries.Add(1)
	}
	parsed, err := event.Interpret(e.Payload, e.ID, e.Source, source.Resource, e.Received, source.Rule)
	if err != nil {
		discard("invalid_payload")
		return 0
	}
	start := time.Now()
	result := a.client.Send(ctx, source.LiteToken, source.Dataset, parsed)
	a.Metrics.DeliveryNS.Add(uint64(time.Since(start)))
	a.Metrics.DeliveryCalls.Add(1)
	if result.Confirmed {
		if err = a.Queue.Delete(ctx, e.ID); err != nil {
			a.localError("delete_confirmed")
			return a.Config.RetryMin.Value()
		}
		a.Metrics.Delivered.Add(1)
		return 0
	}
	a.Metrics.Failures.Add(1)
	if ctx.Err() != nil {
		return 0
	}
	a.logger.Warn("delivery not confirmed", "event_id", e.ID, "reason", result.Reason, "status", result.Status, "attempt", e.Attempts)
	if result.Permanent {
		discard("remote_permanent")
		return 0
	}
	if e.Attempts >= a.Config.MaxAttempts {
		discard("attempts")
		return 0
	}
	wait := min(max(a.backoff(e.Attempts), result.RetryAfter), a.Config.RetryMax.Value())
	if err = a.Queue.Retry(ctx, e.ID, time.Now().Add(wait)); err != nil {
		a.localError("retry")
	}
	return wait
}
