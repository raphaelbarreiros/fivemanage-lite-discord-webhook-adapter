package config_test

import (
	"encoding/json"
	"local/fivemanage-discord-adapter/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceCredentialsAndLegacyRejection(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "synthetic-secret")
	if err := os.WriteFile(secret, []byte("synthetic-configuration-secret-only"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{"lite_url": "https://lite.example.invalid", "sources": []any{
		map[string]any{"id": "a", "secret_file": secret, "lite_token_file": secret, "dataset": "shared", "resource": "a"},
		map[string]any{"id": "b", "secret_file": secret, "lite_token_file": secret, "dataset": "shared", "resource": "b"}}}
	path := filepath.Join(dir, "config.json")
	save := func() {
		t.Helper()
		b, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if _, err := config.Load(path); err != nil {
		t.Fatalf("per-source credentials rejected: %v", err)
	}
	valid["lite_token_file"] = secret
	save()
	if _, err := config.Load(path); err == nil {
		t.Fatal("legacy global credentials silently accepted")
	}
	delete(valid, "lite_token_file")
	source := valid["sources"].([]any)[1].(map[string]any)
	delete(source, "lite_token_file")
	save()
	if _, err := config.Load(path); err == nil {
		t.Fatal("source without explicit destination credential accepted")
	}
	source["lite_token_file"] = filepath.Join(dir, "missing")
	save()
	if _, err := config.Load(path); err == nil {
		t.Fatal("missing token file accepted")
	}
}
