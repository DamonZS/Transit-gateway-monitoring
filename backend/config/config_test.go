package config

import (
	"path/filepath"
	"testing"
)

func TestLoadAppliesUpstreamDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Upstream.TimeoutSeconds != DefaultUpstreamTimeoutSeconds {
		t.Fatalf("timeout seconds = %d", cfg.Upstream.TimeoutSeconds)
	}
	if cfg.Upstream.UserAgent != DefaultUpstreamUserAgent {
		t.Fatalf("user agent = %q", cfg.Upstream.UserAgent)
	}
	if !cfg.Pricing.Enabled || cfg.Pricing.RemoteURL != DefaultPricingRemoteURL {
		t.Fatalf("pricing = %#v", cfg.Pricing)
	}
}

func TestUpstreamConfigWithDefaultsKeepsCustomUserAgent(t *testing.T) {
	cfg := UpstreamConfig{
		TimeoutSeconds: 0,
		UserAgent:      "custom-agent",
	}.WithDefaults()
	if cfg.TimeoutSeconds != DefaultUpstreamTimeoutSeconds {
		t.Fatalf("timeout seconds = %d", cfg.TimeoutSeconds)
	}
	if cfg.UserAgent != "custom-agent" {
		t.Fatalf("user agent = %q", cfg.UserAgent)
	}
}

func TestGatewayConfigWithDefaults(t *testing.T) {
	cfg := GatewayConfig{}.WithDefaults()
	if cfg.TempPauseSeconds != DefaultGatewayTempPauseSeconds {
		t.Fatalf("temp pause = %d", cfg.TempPauseSeconds)
	}
	if cfg.ForwardTimeoutSeconds != DefaultGatewayForwardTimeoutSeconds {
		t.Fatalf("forward timeout = %d", cfg.ForwardTimeoutSeconds)
	}
	if cfg.RouteBatchConcurrency != DefaultGatewayRouteBatchConcurrency {
		t.Fatalf("batch concurrency = %d", cfg.RouteBatchConcurrency)
	}
	custom := GatewayConfig{RouteBatchConcurrency: 16, ForwardTimeoutSeconds: 120}.WithDefaults()
	if custom.RouteBatchConcurrency != 16 || custom.ForwardTimeoutSeconds != 120 {
		t.Fatalf("custom = %#v", custom)
	}
	if custom.ModelsCacheTTLSeconds != DefaultGatewayModelsCacheTTLSeconds {
		t.Fatalf("models cache ttl = %d", custom.ModelsCacheTTLSeconds)
	}
}
