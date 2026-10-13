// Package config loads trusted server-side configuration. No event controls routing.
package config

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}
func (d Duration) Value() time.Duration { return time.Duration(d) }

type Rule struct {
	EventType string `json:"event_type"`
	// Exact embed field names, first matching value; original repeats remain in raw payload.
	Fields map[string]string `json:"fields"`
}
type Source struct {
	ID            string `json:"id"`
	SecretFile    string `json:"secret_file"`
	LiteTokenFile string `json:"lite_token_file"`
	LiteToken     string `json:"-"`
	Dataset       string `json:"dataset"`
	Resource      string `json:"resource"`
	Rule          Rule   `json:"rule"`
	Secret        string `json:"-"`
}
type Queue struct {
	MaxMessages         int64    `json:"max_messages"`
	MaxBytes            int64    `json:"max_bytes"`
	MaxAge              Duration `json:"max_age"`
	Overflow            string   `json:"overflow"`
	MinFreeBytes        uint64   `json:"min_free_bytes"`
	MaxDiskBytes        int64    `json:"max_disk_bytes"`
	BusyTimeout         Duration `json:"busy_timeout"`
	MaintenanceInterval Duration `json:"maintenance_interval"`
	VacuumPages         int      `json:"vacuum_pages"`
}
type Config struct {
	Listen           string   `json:"listen"`
	AdminListen      string   `json:"admin_listen"`
	DataDir          string   `json:"data_dir"`
	LiteURL          string   `json:"lite_url"`
	AllowHTTP        bool     `json:"allow_http"`
	MaxBodyBytes     int64    `json:"max_body_bytes"`
	MaxInflight      int      `json:"max_inflight"`
	AdmissionTimeout Duration `json:"admission_timeout"`
	HTTPTimeout      Duration `json:"http_timeout"`
	ShutdownTimeout  Duration `json:"shutdown_timeout"`
	RetryMin         Duration `json:"retry_min"`
	RetryMax         Duration `json:"retry_max"`
	MaxAttempts      int      `json:"max_attempts"`
	SendInterval     Duration `json:"send_interval"`
	Queue            Queue    `json:"queue"`
	Sources          []Source `json:"sources"`
}

func Defaults() Config {
	return Config{Listen: ":8080", AdminListen: "127.0.0.1:9090", DataDir: "data", MaxBodyBytes: 256 << 10, MaxInflight: 32,
		AdmissionTimeout: Duration(5 * time.Second), HTTPTimeout: Duration(10 * time.Second), ShutdownTimeout: Duration(20 * time.Second),
		RetryMin: Duration(time.Second), RetryMax: Duration(5 * time.Minute), MaxAttempts: 100, SendInterval: Duration(100 * time.Millisecond),
		Queue: Queue{MaxMessages: 10000, MaxBytes: 128 << 20, MaxAge: Duration(24 * time.Hour), Overflow: "reject_new", MinFreeBytes: 256 << 20, MaxDiskBytes: 512 << 20, BusyTimeout: Duration(500 * time.Millisecond), MaintenanceInterval: Duration(30 * time.Second), VacuumPages: 256}}
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var normalized = map[string]bool{"player_session_id": true, "player_character_id": true, "player_account_id": true, "player_discord_id": true, "target_session_id": true, "target_character_id": true, "target_account_id": true, "target_discord_id": true, "item": true, "quantity": true, "value": true, "event_time": true}

func Load(path string) (Config, error) {
	c := Defaults()
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("config: cannot open file")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil {
		return c, errors.New("config: invalid JSON or unknown option")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return c, errors.New("config: trailing data")
	}
	if err = c.Validate(); err != nil {
		return c, err
	}
	for i := range c.Sources {
		c.Sources[i].LiteToken, err = ReadSecret(c.Sources[i].LiteTokenFile)
		if err != nil {
			return c, err
		}
		c.Sources[i].Secret, err = ReadSecret(c.Sources[i].SecretFile)
		if err != nil {
			return c, err
		}
	}
	return c, nil
}
func ReadSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("secret: cannot open file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	s := strings.TrimSpace(string(b))
	if err != nil || len(s) < 24 || len(b) > 4096 || strings.ContainsAny(s, "\r\n\t ") || strings.Contains(s, "PLACEHOLDER") {
		return "", errors.New("secret: expected 24..4096 non-whitespace bytes, not a placeholder")
	}
	return s, nil
}
func (c Config) Validate() error {
	bad := func() error {
		return errors.New("config: invalid limits, addresses, source or destination; see README")
	}
	for _, a := range []string{c.Listen, c.AdminListen} {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return bad()
		}
	}
	u, err := url.Parse(c.LiteURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && c.AllowHTTP)) {
		return bad()
	}
	if c.DataDir == "" || c.MaxBodyBytes < 1 || c.MaxBodyBytes > 4<<20 || c.MaxInflight < 1 || c.MaxInflight > 1024 || c.Queue.MaxMessages < 1 || c.Queue.MaxBytes < c.MaxBodyBytes || c.Queue.MaxDiskBytes < c.Queue.MaxBytes || c.Queue.MinFreeBytes == 0 || c.Queue.VacuumPages < 1 || c.Queue.VacuumPages > 4096 || c.MaxAttempts < 1 {
		return bad()
	}
	if c.Queue.Overflow != "reject_new" && c.Queue.Overflow != "drop_oldest" {
		return bad()
	}
	for _, d := range []Duration{c.AdmissionTimeout, c.HTTPTimeout, c.ShutdownTimeout, c.RetryMin, c.RetryMax, c.SendInterval, c.Queue.MaxAge, c.Queue.BusyTimeout, c.Queue.MaintenanceInterval} {
		if d <= 0 || d.Value() > 365*24*time.Hour {
			return bad()
		}
	}
	if c.RetryMax < c.RetryMin || c.Queue.BusyTimeout > c.AdmissionTimeout || c.ShutdownTimeout < c.AdmissionTimeout {
		return bad()
	}
	if len(c.Sources) == 0 || len(c.Sources) > 256 {
		return bad()
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if !identifier.MatchString(s.ID) || seen[s.ID] || !identifier.MatchString(s.Dataset) || !identifier.MatchString(s.Resource) || s.SecretFile == "" || s.LiteTokenFile == "" {
			return bad()
		}
		seen[s.ID] = true
		if s.Rule.EventType != "" && !identifier.MatchString(s.Rule.EventType) {
			return bad()
		}
		for k, v := range s.Rule.Fields {
			if !normalized[k] || v == "" || len(v) > 256 {
				return bad()
			}
		}
	}
	return nil
}
