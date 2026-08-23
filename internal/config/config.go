package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HubURL                   string        `yaml:"hub_url"`
	Token                    string        `yaml:"token"`
	MoonrakerURL             string        `yaml:"moonraker_url"`
	PrinterID                string        `yaml:"printer_id"`
	HeartbeatIntervalSeconds int           `yaml:"heartbeat_interval_seconds"`
	HeartbeatInterval        time.Duration `yaml:"-"`
	LogFile                  string        `yaml:"log_file"`
	LogLevel                 string        `yaml:"log_level"`
	SpoolmanProxyPort        int           `yaml:"spoolman_proxy_port"` // puerto del proxy Spoolman local; 0 = deshabilitado
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("leer archivo de config: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsear archivo de config: %w", err)
	}

	if cfg.MoonrakerURL == "" {
		return nil, errors.New("moonraker_url es obligatorio (ej. ws://localhost:7125/websocket)")
	}
	if cfg.HeartbeatIntervalSeconds <= 0 {
		cfg.HeartbeatIntervalSeconds = 30
	}
	cfg.HeartbeatInterval = time.Duration(cfg.HeartbeatIntervalSeconds) * time.Second

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	if cfg.SpoolmanProxyPort <= 0 {
		cfg.SpoolmanProxyPort = 8001
	}

	return cfg, nil
}
