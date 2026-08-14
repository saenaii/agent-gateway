package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.Server.HTTPAddr)
	}
	if cfg.Ollama.Model != "qwen2.5:0.5b" {
		t.Errorf("Model = %q, want qwen2.5:0.5b", cfg.Ollama.Model)
	}
	if cfg.Ollama.RequestTimeout.Duration != 5*time.Second {
		t.Errorf("RequestTimeout = %v, want 5s", cfg.Ollama.RequestTimeout)
	}
}

func TestLoadYAMLOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  http_addr: \":9999\"\nollama:\n  model: \"llama3.2:1b\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr = %q, want :9999", cfg.Server.HTTPAddr)
	}
	if cfg.Ollama.Model != "llama3.2:1b" {
		t.Errorf("Model = %q, want llama3.2:1b", cfg.Ollama.Model)
	}
	if cfg.Ollama.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL = %q, want default", cfg.Ollama.BaseURL)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://ollama:11434")
	t.Setenv("OLLAMA_REQUEST_TIMEOUT", "2s")
	t.Setenv("OLLAMA_RETRY_ATTEMPTS", "3")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ollama.BaseURL != "http://ollama:11434" {
		t.Errorf("BaseURL = %q", cfg.Ollama.BaseURL)
	}
	if cfg.Ollama.RequestTimeout.Duration != 2*time.Second {
		t.Errorf("RequestTimeout = %v", cfg.Ollama.RequestTimeout)
	}
	if cfg.Ollama.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d", cfg.Ollama.RetryAttempts)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nGATEWAY_HTTP_ADDR=:7777\n\nOLLAMA_MODEL=qwen3:0.6b\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPAddr != ":7777" {
		t.Errorf("HTTPAddr = %q, want :7777", cfg.Server.HTTPAddr)
	}
	if cfg.Ollama.Model != "qwen3:0.6b" {
		t.Errorf("Model = %q, want qwen3:0.6b", cfg.Ollama.Model)
	}
}

func TestDotEnvDoesNotOverrideRealEnv(t *testing.T) {
	t.Setenv("GATEWAY_HTTP_ADDR", ":1234")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("GATEWAY_HTTP_ADDR=:7777\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPAddr != ":1234" {
		t.Errorf("HTTPAddr = %q, want :1234 (real env wins)", cfg.Server.HTTPAddr)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.GRPCAddr != ":9090" {
		t.Errorf("GRPCAddr = %q, want :9090", cfg.Server.GRPCAddr)
	}
}

func TestLoadLayer2Defaults(t *testing.T) {
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Layer2.DefaultProvider != "opencode" {
		t.Errorf("DefaultProvider = %q, want opencode", cfg.Layer2.DefaultProvider)
	}
	if cfg.Layer2.RequestTimeout.Duration != 60*time.Second {
		t.Errorf("RequestTimeout = %v, want 60s", cfg.Layer2.RequestTimeout)
	}
	if cfg.Layer2.RetryAttempts != 1 {
		t.Errorf("RetryAttempts = %d, want 1", cfg.Layer2.RetryAttempts)
	}
	p, ok := cfg.Layer2.Providers["opencode"]
	if !ok {
		t.Fatal("opencode provider missing from defaults")
	}
	if p.Type != "opencode" || p.BaseURL != "http://localhost:4096" {
		t.Errorf("provider = %+v", p)
	}
}

func TestLoadLayer2YAMLAndEnv(t *testing.T) {
	t.Setenv("LAYER2_OPENCODE_MODEL", "opencode/deepseek-v4-flash-free")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `layer2:
  default_provider: "custom"
  request_timeout: 90s
  providers:
    opencode:
      type: "opencode"
      base_url: "http://oc:4096"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Layer2.DefaultProvider != "custom" {
		t.Errorf("DefaultProvider = %q, want custom", cfg.Layer2.DefaultProvider)
	}
	if cfg.Layer2.RequestTimeout.Duration != 90*time.Second {
		t.Errorf("RequestTimeout = %v, want 90s", cfg.Layer2.RequestTimeout)
	}
	p := cfg.Layer2.Providers["opencode"]
	if p.BaseURL != "http://oc:4096" {
		t.Errorf("BaseURL = %q, want http://oc:4096 (from YAML)", p.BaseURL)
	}
	if p.Model != "opencode/deepseek-v4-flash-free" {
		t.Errorf("Model = %q, want env override", p.Model)
	}
}
