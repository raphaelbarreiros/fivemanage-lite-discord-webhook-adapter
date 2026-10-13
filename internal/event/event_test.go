package event_test

import (
	"bytes"
	"encoding/base64"
	"local/fivemanage-discord-adapter/internal/config"
	"local/fivemanage-discord-adapter/internal/event"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOriginalRoundTripAndExplicitRule(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/discord.json")
	if err != nil {
		t.Fatal(err)
	}
	received := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	out, err := event.Interpret(raw, "stable-id", "trusted-source", "trusted-resource", received, config.Rule{EventType: "inventory", Fields: map[string]string{"player_character_id": "Personagem", "player_discord_id": "Discord", "quantity": "Quantidade"}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Metadata["discord_payload_b64"])
	if err != nil || !bytes.Equal(decoded, raw) {
		t.Fatal("original bytes did not round-trip")
	}
	if out.Resource != "trusted-resource" || out.Metadata["player_character_id"] != "9007199254740993123" || out.Metadata["player_discord_id"] != "18446744073709551615" || out.Metadata["quantity"] != "12" {
		t.Fatalf("routing or number precision lost: %v", out.Metadata)
	}
	if !strings.Contains(out.Message, "Quantidade: 12\nQuantidade: 99") || !strings.Contains(out.Message, "Segundo embed") || !strings.Contains(out.Message, "日本語") {
		t.Fatal("order, repeats, unicode or embeds lost")
	}
	if out.Metadata["adapter_received_at"] != "2026-10-13T12:00:00Z" || out.Metadata["discord_embed_0_timestamp"] != "2026-01-02T03:04:05-03:00" {
		t.Fatal("event and receipt timestamps conflated")
	}
}
func TestUnknownFormatIsReadableWithoutInventedBusinessFields(t *testing.T) {
	out, err := event.Interpret([]byte(`{"embeds":[{"future":{"x":true}}]}`), "id", "source", "resource", time.Now(), config.Rule{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Message, "formato não reconhecido") {
		t.Fatal(out.Message)
	}
	if _, ok := out.Metadata["event_type"]; ok {
		t.Fatal("invented event type")
	}
}
func TestValidationBoundaries(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"content":42}`, `{"content":"x"} {}`, `{"embeds":[null]}`, `{"embeds":{}}`} {
		if _, err := event.Decode([]byte(raw)); err != event.ErrFormat {
			t.Errorf("expected invalid format: %s", raw)
		}
	}
	for _, raw := range []string{`{"content":"x","attachments":[]}`, `{"content":"x","poll":{}}`, `{"content":"x","tts":true}`, `{"content":"x","flags":0}`} {
		if _, err := event.Decode([]byte(raw)); err != event.ErrUnsupported {
			t.Errorf("expected unsupported: %s", raw)
		}
	}
	out, err := event.Interpret([]byte(`{"content":"plain text","unknown":{"n":12345678901234567890}}`), "id", "source", "resource", time.Now(), config.Rule{})
	if err != nil || out.Message != "plain text" {
		t.Fatal("simple text rejected", err)
	}
}
