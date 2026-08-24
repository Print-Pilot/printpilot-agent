package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Print-Pilot/printpilot-agent/internal/config"
)

// claves de config editables por el CLI y su tipo (para parsear el valor).
var configKeys = map[string]string{
	"hub_url":                    "string",
	"token":                      "string",
	"moonraker_url":              "string",
	"printer_id":                 "string",
	"heartbeat_interval_seconds": "int",
	"log_file":                   "string",
	"log_level":                  "string",
	"spoolman_proxy_port":        "int",
	"go2rtc_url":                 "string",
	"camera_stream":              "string",
	"webrtc_timeout_seconds":     "int",
}

// cmdConfig lee o edita la config:
//
//	printpilot config get [clave]     (sin clave: muestra todo, token enmascarado)
//	printpilot config set <clave> <valor>
func cmdConfig(configPath string, args []string) int {
	if !requireRoot() {
		return 1
	}

	if len(args) == 0 {
		configUsage()
		return 2
	}

	switch args[0] {
	case "get":
		if len(args) > 1 {
			return configGet(configPath, args[1])
		}
		return configGetAll(configPath)
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "uso: printpilot config set <clave> <valor>")
			return 2
		}
		return configSet(configPath, args[1], strings.Join(args[2:], " "))
	case "-h", "--help", "help":
		configUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %s\n", args[0])
		configUsage()
		return 2
	}
}

func configUsage() {
	fmt.Println(`printpilot config

Uso:
  printpilot config get            muestra toda la config (token enmascarado)
  printpilot config get <clave>    muestra una clave puntual
  printpilot config set <clave> <valor>  cambia un valor y reinicia el servicio

Claves: hub_url, token, moonraker_url, printer_id, heartbeat_interval_seconds,
log_file, log_level, spoolman_proxy_port, go2rtc_url, camera_stream,
webrtc_timeout_seconds

Nota: config set reinicia el servicio automáticamente para aplicar el cambio.`)
}

func configGetAll(configPath string) int {
	if !fileExists(configPath) {
		fmt.Fprintln(os.Stderr, "No hay config. Corré el instalador o usa 'printpilot printer new'.")
		return 1
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo leer la config: %v\n", err)
		return 1
	}

	rows := [][2]string{
		{"hub_url", cfg.HubURL},
		{"token", maskToken(cfg.Token)},
		{"moonraker_url", cfg.MoonrakerURL},
		{"printer_id", cfg.PrinterID},
		{"heartbeat_interval_seconds", strconv.Itoa(cfg.HeartbeatIntervalSeconds)},
		{"log_file", cfg.LogFile},
		{"log_level", cfg.LogLevel},
		{"spoolman_proxy_port", strconv.Itoa(cfg.SpoolmanProxyPort)},
		{"go2rtc_url", cfg.Go2rtcURL},
		{"camera_stream", cfg.CameraStream},
		{"webrtc_timeout_seconds", strconv.Itoa(cfg.WebrtcTimeoutSeconds)},
	}

	width := 0
	for _, r := range rows {
		if len(r[0]) > width {
			width = len(r[0])
		}
	}
	for _, r := range rows {
		fmt.Printf("  %-*s  %s\n", width, r[0], r[1])
	}

	return 0
}

func configGet(configPath, key string) int {
	if _, ok := configKeys[key]; !ok {
		fmt.Fprintf(os.Stderr, "clave desconocida: %s\n", key)
		return 2
	}
	if !fileExists(configPath) {
		fmt.Fprintln(os.Stderr, "No hay config.")
		return 1
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo leer la config: %v\n", err)
		return 1
	}

	value := configField(cfg, key)
	if key == "token" {
		value = maskToken(value)
	}
	fmt.Println(value)

	return 0
}

func configSet(configPath, key, value string) int {
	if !requireRoot() {
		return 1
	}

	kind, ok := configKeys[key]
	if !ok {
		fmt.Fprintf(os.Stderr, "clave desconocida: %s\n", key)
		return 2
	}

	if !fileExists(configPath) {
		fmt.Fprintln(os.Stderr, "No hay config.")
		return 1
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo leer la config: %v\n", err)
		return 1
	}

	switch kind {
	case "int":
		n, perr := strconv.Atoi(strings.TrimSpace(value))
		if perr != nil {
			fmt.Fprintf(os.Stderr, "'%s' debe ser un número entero\n", value)
			return 2
		}
		switch key {
		case "heartbeat_interval_seconds":
			cfg.HeartbeatIntervalSeconds = n
		case "spoolman_proxy_port":
			cfg.SpoolmanProxyPort = n
		case "webrtc_timeout_seconds":
			cfg.WebrtcTimeoutSeconds = n
		}
	default:
		switch key {
		case "hub_url":
			cfg.HubURL = value
		case "token":
			cfg.Token = value
		case "moonraker_url":
			cfg.MoonrakerURL = value
		case "printer_id":
			cfg.PrinterID = value
		case "log_file":
			cfg.LogFile = value
		case "log_level":
			cfg.LogLevel = value
		case "go2rtc_url":
			cfg.Go2rtcURL = value
		case "camera_stream":
			cfg.CameraStream = value
		}
	}

	if err := cfg.Save(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo guardar la config: %v\n", err)
		return 1
	}

	if key == "token" {
		fmt.Println("token actualizado (no se muestra por seguridad).")
	} else {
		fmt.Printf("%s = %s\n", key, value)
	}
	restartAfterChange()

	return 0
}

func configField(cfg *config.Config, key string) string {
	switch key {
	case "hub_url":
		return cfg.HubURL
	case "token":
		return cfg.Token
	case "moonraker_url":
		return cfg.MoonrakerURL
	case "printer_id":
		return cfg.PrinterID
	case "heartbeat_interval_seconds":
		return strconv.Itoa(cfg.HeartbeatIntervalSeconds)
	case "log_file":
		return cfg.LogFile
	case "log_level":
		return cfg.LogLevel
	case "spoolman_proxy_port":
		return strconv.Itoa(cfg.SpoolmanProxyPort)
	case "go2rtc_url":
		return cfg.Go2rtcURL
	case "camera_stream":
		return cfg.CameraStream
	case "webrtc_timeout_seconds":
		return strconv.Itoa(cfg.WebrtcTimeoutSeconds)
	}
	return ""
}
