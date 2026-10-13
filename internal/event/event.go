// Package event validates the supported Discord subset and interprets it without I/O.
package event

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"local/fivemanage-discord-adapter/internal/config"
	"strings"
	"time"
	"unicode/utf8"
)

const InterpreterVersion = "discord-generic/1"

var ErrFormat = errors.New("invalid webhook JSON")
var ErrUnsupported = errors.New("unsupported Discord feature")

type Log struct {
	Level    string            `json:"level"`
	Message  string            `json:"message"`
	Resource string            `json:"resource"`
	Metadata map[string]string `json:"metadata"`
}

// Decode uses numbers as lexemes and retains the original bytes separately.
func Decode(raw []byte) (map[string]any, error) {
	if !utf8.Valid(raw) {
		return nil, ErrFormat
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil || v == nil {
		return nil, ErrFormat
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrFormat
	}
	// These keys request features, not merely unknown log properties.
	for _, key := range []string{"attachments", "files", "components", "poll", "thread_name", "thread_id", "applied_tags", "payload_json", "flags"} {
		if _, ok := v[key]; ok {
			return nil, ErrUnsupported
		}
	}
	if t, ok := v["tts"]; ok && t != false {
		return nil, ErrUnsupported
	}
	has := false
	if c, ok := v["content"]; ok {
		str, yes := c.(string)
		if !yes {
			return nil, ErrFormat
		}
		has = str != ""
	}
	if embeds, ok := v["embeds"]; ok {
		a, yes := embeds.([]any)
		if !yes {
			return nil, ErrFormat
		}
		for _, e := range a {
			if _, yes := e.(map[string]any); !yes {
				return nil, ErrFormat
			}
		}
		has = has || len(a) > 0
	}
	if !has {
		return nil, ErrFormat
	}
	return v, nil
}
func Interpret(raw []byte, id, source, resource string, received time.Time, rule config.Rule) (Log, error) {
	v, err := Decode(raw)
	if err != nil {
		return Log{}, err
	}
	var text strings.Builder
	add := func(s string) {
		if s != "" {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(s)
		}
	}
	if s, ok := v["content"].(string); ok {
		add(s)
	}
	metadata := map[string]string{"adapter_event_id": id, "adapter_source": source, "adapter_received_at": received.UTC().Format(time.RFC3339Nano), "adapter_interpreter": InterpreterVersion, "discord_payload_b64": base64.StdEncoding.EncodeToString(raw)}
	if rule.EventType != "" {
		metadata["event_type"] = rule.EventType
	}
	if embeds, ok := v["embeds"].([]any); ok {
		for i, e := range embeds {
			embed := e.(map[string]any)
			for _, key := range []string{"title", "description", "url"} {
				if s, ok := embed[key].(string); ok {
					add(s)
				}
			}
			if s, ok := embed["timestamp"].(string); ok {
				metadata[fmt.Sprintf("discord_embed_%d_timestamp", i)] = s
			}
			if fields, ok := embed["fields"].([]any); ok {
				for _, field := range fields {
					f, ok := field.(map[string]any)
					if !ok {
						continue
					}
					name, _ := f["name"].(string)
					value := scalar(f["value"])
					if name != "" || value != "" {
						add(name + ": " + value)
					}
					for normalized, match := range rule.Fields {
						if match == name && value != "" {
							if _, exists := metadata[normalized]; !exists {
								metadata[normalized] = value
							}
						}
					}
				}
			}
		}
	}
	if text.Len() == 0 {
		add("Evento Discord recebido (formato não reconhecido; payload original preservado)")
	}
	return Log{Level: "info", Message: text.String(), Resource: resource, Metadata: metadata}, nil
}
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}
