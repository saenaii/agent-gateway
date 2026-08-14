// Package config loads gateway configuration from YAML, .env, and environment
// variables. Precedence (low to high): defaults, config.yaml, .env, environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to support YAML strings like "5s".
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses a duration from a YAML scalar.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar, got kind %d", node.Kind)
	}
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", node.Value, err)
	}
	d.Duration = v
	return nil
}

// Config is the root gateway configuration.
type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Ollama         OllamaConfig         `yaml:"ollama"`
	Layer2         Layer2Config         `yaml:"layer2"`
	WorkerPool     WorkerPoolConfig     `yaml:"worker_pool"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Metrics        MetricsConfig        `yaml:"metrics"`
	Dashboard      DashboardConfig      `yaml:"dashboard"`
}

// ServerConfig holds HTTP/gRPC listener settings.
type ServerConfig struct {
	HTTPAddr     string   `yaml:"http_addr"`
	GRPCAddr     string   `yaml:"grpc_addr"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
}

// OllamaConfig configures the Layer 1 local model endpoint.
type OllamaConfig struct {
	BaseURL        string   `yaml:"base_url"`
	Model          string   `yaml:"model"`
	RequestTimeout Duration `yaml:"request_timeout"`
	RetryAttempts  int      `yaml:"retry_attempts"`
}

// Layer2Config configures the escalation path for complex intents.
type Layer2Config struct {
	DefaultProvider string                    `yaml:"default_provider"`
	RequestTimeout  Duration                  `yaml:"request_timeout"`
	RetryAttempts   int                       `yaml:"retry_attempts"`
	Providers       map[string]ProviderConfig `yaml:"providers"`
}

// ProviderConfig describes one Layer 2 upstream backend. Type selects the
// backend implementation (e.g. "opencode"; "openai" and "bedrock" planned).
type ProviderConfig struct {
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

// WorkerPoolConfig sizes the classify job pool.
type WorkerPoolConfig struct {
	Size      int `yaml:"size"`
	QueueSize int `yaml:"queue_size"`
}

// CircuitBreakerConfig tunes the Layer 1 circuit breaker.
type CircuitBreakerConfig struct {
	FailureThreshold int      `yaml:"failure_threshold"`
	Cooldown         Duration `yaml:"cooldown"`
}

// MetricsConfig tunes the in-memory metrics collector.
type MetricsConfig struct {
	Window         Duration `yaml:"window"`
	MaxSamples     int      `yaml:"max_samples"`
	RequestLogSize int      `yaml:"request_log_size"`
}

// DashboardConfig toggles the embedded web UI.
type DashboardConfig struct {
	Enabled bool `yaml:"enabled"`
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			HTTPAddr:     ":8080",
			GRPCAddr:     ":9090",
			ReadTimeout:  Duration{30 * time.Second},
			WriteTimeout: Duration{60 * time.Second},
		},
		Ollama: OllamaConfig{
			BaseURL:        "http://localhost:11434",
			Model:          "qwen2.5:0.5b",
			RequestTimeout: Duration{5 * time.Second},
			RetryAttempts:  1,
		},
		Layer2: Layer2Config{
			DefaultProvider: "opencode",
			RequestTimeout:  Duration{60 * time.Second},
			RetryAttempts:   1,
			Providers: map[string]ProviderConfig{
				"opencode": {
					Type:    "opencode",
					BaseURL: "http://localhost:4096",
				},
			},
		},
		WorkerPool: WorkerPoolConfig{Size: 16, QueueSize: 256},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 3,
			Cooldown:         Duration{30 * time.Second},
		},
		Metrics: MetricsConfig{
			Window:         Duration{10 * time.Minute},
			MaxSamples:     10000,
			RequestLogSize: 200,
		},
		Dashboard: DashboardConfig{Enabled: true},
	}
}

// Load builds the configuration: defaults, then config.yaml (if present),
// then .env, then environment variables.
func Load(yamlPath, envPath string) (*Config, error) {
	cfg := defaults()
	if yamlPath != "" {
		if err := applyYAML(cfg, yamlPath); err != nil {
			return nil, err
		}
	}
	if envPath != "" {
		if err := loadDotEnv(envPath); err != nil {
			return nil, err
		}
	}
	applyEnv(cfg)
	return cfg, nil
}

func applyYAML(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// loadDotEnv parses KEY=VALUE lines from path into the process environment.
// Existing environment variables are never overwritten.
func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read env file %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("set env %s: %w", k, err)
		}
	}
	return nil
}

func applyEnv(cfg *Config) {
	cfg.Server.HTTPAddr = envString("GATEWAY_HTTP_ADDR", cfg.Server.HTTPAddr)
	cfg.Server.GRPCAddr = envString("GATEWAY_GRPC_ADDR", cfg.Server.GRPCAddr)
	cfg.Server.ReadTimeout = envDuration("GATEWAY_READ_TIMEOUT", cfg.Server.ReadTimeout.Duration)
	cfg.Server.WriteTimeout = envDuration("GATEWAY_WRITE_TIMEOUT", cfg.Server.WriteTimeout.Duration)

	cfg.Ollama.BaseURL = envString("OLLAMA_BASE_URL", cfg.Ollama.BaseURL)
	cfg.Ollama.Model = envString("OLLAMA_MODEL", cfg.Ollama.Model)
	cfg.Ollama.RequestTimeout = envDuration("OLLAMA_REQUEST_TIMEOUT", cfg.Ollama.RequestTimeout.Duration)
	cfg.Ollama.RetryAttempts = envInt("OLLAMA_RETRY_ATTEMPTS", cfg.Ollama.RetryAttempts)

	cfg.Layer2.DefaultProvider = envString("LAYER2_DEFAULT_PROVIDER", cfg.Layer2.DefaultProvider)
	cfg.Layer2.RequestTimeout = envDuration("LAYER2_REQUEST_TIMEOUT", cfg.Layer2.RequestTimeout.Duration)
	cfg.Layer2.RetryAttempts = envInt("LAYER2_RETRY_ATTEMPTS", cfg.Layer2.RetryAttempts)
	if p, ok := cfg.Layer2.Providers["opencode"]; ok {
		p.BaseURL = envString("LAYER2_OPENCODE_BASE_URL", p.BaseURL)
		p.Model = envString("LAYER2_OPENCODE_MODEL", p.Model)
		cfg.Layer2.Providers["opencode"] = p
	}
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return Duration{d}
		}
	}
	return Duration{fallback}
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
