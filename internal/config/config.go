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

	// WebRTC (cámara en vivo). Go2rtcURL vacío deshabilita el streaming.
	Go2rtcURL            string        `yaml:"go2rtc_url"`             // ej. http://localhost:1984
	CameraStream         string        `yaml:"camera_stream"`          // stream de go2rtc a servir para camera_id "default"; vacío = usar el camera_id
	WebrtcTimeoutSeconds int           `yaml:"webrtc_timeout_seconds"` // negociación máx; 0 = 15
	WebrtcTimeout        time.Duration `yaml:"-"`
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

	if cfg.Go2rtcURL != "" && cfg.WebrtcTimeoutSeconds <= 0 {
		cfg.WebrtcTimeoutSeconds = 15
	}
	cfg.WebrtcTimeout = time.Duration(cfg.WebrtcTimeoutSeconds) * time.Second

	return cfg, nil
}
