package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigValid(t *testing.T) {
	path := writeTempConfig(t, "hub_url: wss://hub.example.com/ws\ntoken: \"secret-token\"\nmoonraker_url: ws://localhost:7125/websocket\nprinter_id: test-printer\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HubURL != "wss://hub.example.com/ws" {
		t.Errorf("HubURL = %q, esperaba %q", cfg.HubURL, "wss://hub.example.com/ws")
	}
	if cfg.Token != "secret-token" {
		t.Errorf("Token = %q, esperaba %q", cfg.Token, "secret-token")
	}
	if cfg.MoonrakerURL != "ws://localhost:7125/websocket" {
		t.Errorf("MoonrakerURL = %q, esperaba %q", cfg.MoonrakerURL, "ws://localhost:7125/websocket")
	}
	if cfg.PrinterID != "test-printer" {
		t.Errorf("PrinterID = %q, esperaba %q", cfg.PrinterID, "test-printer")
	}
}

func TestLoadConfigMissingMoonrakerURL(t *testing.T) {
	path := writeTempConfig(t, "hub_url: wss://hub.example.com/ws\nprinter_id: test\n")

	if _, err := LoadConfig(path); err == nil {
		t.Fatal("esperaba error por moonraker_url vacío")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig("/no/existe/config.yaml"); err == nil {
		t.Fatal("esperaba error por archivo inexistente")
	}
}

func TestLoadConfigDefaultHeartbeat(t *testing.T) {
	path := writeTempConfig(t, "moonraker_url: ws://localhost:7125/websocket\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HeartbeatIntervalSeconds != 30 {
		t.Errorf("HeartbeatIntervalSeconds = %d, esperaba 30", cfg.HeartbeatIntervalSeconds)
	}
}

func TestLoadConfigDefaultLogLevel(t *testing.T) {
	path := writeTempConfig(t, "moonraker_url: ws://localhost:7125/websocket\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, esperaba info", cfg.LogLevel)
	}
}

func TestLoadConfigLogFileAndLevel(t *testing.T) {
	path := writeTempConfig(t, "moonraker_url: ws://localhost:7125/websocket\nlog_file: \"/tmp/x.log\"\nlog_level: \"debug\"\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LogFile != "/tmp/x.log" {
		t.Errorf("LogFile = %q, esperaba /tmp/x.log", cfg.LogFile)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, esperaba debug", cfg.LogLevel)
	}
}

func TestLoadConfigDefaultSpoolmanPort(t *testing.T) {
	path := writeTempConfig(t, "moonraker_url: ws://localhost:7125/websocket\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SpoolmanProxyPort != 8001 {
		t.Errorf("SpoolmanProxyPort = %d, esperaba 8001", cfg.SpoolmanProxyPort)
	}
}

func TestLoadConfigCustomSpoolmanPort(t *testing.T) {
	path := writeTempConfig(t, "moonraker_url: ws://localhost:7125/websocket\nspoolman_proxy_port: 9000\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SpoolmanProxyPort != 9000 {
		t.Errorf("SpoolmanProxyPort = %d, esperaba 9000", cfg.SpoolmanProxyPort)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("escribir config temporal: %v", err)
	}
	return path
}
