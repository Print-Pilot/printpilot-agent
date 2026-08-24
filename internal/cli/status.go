package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Print-Pilot/printpilot-agent/internal/config"
)

// cmdStatus muestra el estado del servicio, la config y la conectividad
// (Moonraker en vivo + estado de la conexión al hub derivado del log).
func cmdStatus(configPath string) int {
	fmt.Println()
	fmt.Println("Estado de printpilot-agent")
	fmt.Println()

	// --- Servicio ---------------------------------------------------------
	if systemdPresent() && serviceUnitExists() {
		state := serviceState()
		fmt.Println("Servicio")
		fmt.Printf("  estado:          %s\n", state)
		fmt.Printf("  habilitado:      %s\n", serviceEnabled())
		fmt.Printf("  versión:         %s\n", installedVersion())
		fmt.Println()
	} else {
		fmt.Println("Servicio: no instalado (no hay unidad systemd printpilot-agent).")
		fmt.Println()
	}

	// --- Config ------------------------------------------------------------
	if !fileExists(configPath) {
		fmt.Printf("Config: no encontrada en %s. El agente no está configurado.\n", configPath)
		fmt.Println()
		return 1
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("Config: error leyendo %s: %v\n", configPath, err)
		fmt.Println()
		return 1
	}

	fmt.Printf("Config (%s)\n", configPath)
	fmt.Printf("  printer_id:      %s\n", cfg.PrinterID)
	fmt.Printf("  hub_url:         %s\n", cfg.HubURL)
	fmt.Printf("  token:           %s\n", maskToken(cfg.Token))
	fmt.Printf("  moonraker_url:   %s\n", cfg.MoonrakerURL)
	if cfg.Go2rtcURL != "" {
		fmt.Printf("  go2rtc_url:      %s\n", cfg.Go2rtcURL)
		fmt.Printf("  camera_stream:   %s\n", displayOrEmpty(cfg.CameraStream))
	}
	if cfg.SpoolmanProxyPort > 0 {
		fmt.Printf("  spoolman_proxy:  :%d\n", cfg.SpoolmanProxyPort)
	}
	fmt.Println()

	// --- Conectividad ------------------------------------------------------
	fmt.Println("Conectividad")

	// Moonraker: probe HTTP en vivo.
	if online, detail := moonrakerProbe(cfg.MoonrakerURL); online {
		fmt.Printf("  Moonraker:       online — %s\n", detail)
	} else {
		fmt.Printf("  Moonraker:       offline — %s\n", detail)
	}

	// Hub: estado derivado del log del agente.
	hub := hubState(cfg.LogFile)
	switch hub.state {
	case "connected":
		fmt.Println("  Hub:             conectado")
	case "auth-rejected":
		fmt.Println("  Hub:             RECHAZADO — revisá token/printer_id en la config")
	case "reconnecting":
		fmt.Println("  Hub:             reconectando (red caída o hub inalcanzable)")
	default:
		fmt.Println("  Hub:             sin actividad registrada")
	}

	if last := lastHubTraffic(cfg.LogFile); !last.IsZero() {
		fmt.Printf("  Última actividad del hub: hace %s\n", humanDuration(time.Since(last)))
	}
	fmt.Println()

	// --- Log reciente ------------------------------------------------------
	fmt.Println("Log reciente (5 líneas)")
	tailLog(cfg.LogFile, 5)
	fmt.Println()

	if state := serviceState(); state == "inactive" || state == "failed" {
		fmt.Printf("Sugerencia: el servicio está %s. Reinicialo con:\n  sudo systemctl restart %s\n", state, serviceName)
	}

	return 0
}

func displayOrEmpty(s string) string {
	if s == "" {
		return "(vacío — usa el camera_id)"
	}
	return s
}

// moonrakerProbe consulta /server/info de Moonraker con timeout corto.
func moonrakerProbe(wsURL string) (bool, string) {
	base := MoonrakerHTTPBase(wsURL)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/server/info")
	if err != nil {
		return false, "no responde (" + err.Error() + ")"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	var body struct {
		Result struct {
			Klipper struct {
				State   string `json:"state"`
				Version string `json:"version"`
			} `json:"klipper"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return true, "responde pero no se pudo parsear /server/info"
	}

	detail := "Klipper " + body.Result.Klipper.Version
	if body.Result.Klipper.State != "" {
		detail += " (" + body.Result.Klipper.State + ")"
	}
	return true, detail
}

// --- Parsing del log del agente (formato slog de texto) --------------------

// parseSlogLine extrae time/level/msg de una línea estilo:
// time=2026-08-24T12:34:56-03:00 level=INFO msg="conectado al hub" url=...
// Maneja valores con espacios entre comillas (msg="conectado al hub").
func parseSlogLine(line string) (ts, level, msg string) {
	return slogAttr(line, "time"), slogAttr(line, "level"), slogAttr(line, "msg")
}

// slogAttr extrae el valor de un atributo del formato slog de texto,
// respetando valores entre comillas con espacios.
func slogAttr(line, key string) string {
	prefix := key + "="
	i := strings.Index(line, prefix)
	if i < 0 {
		return ""
	}
	rest := line[i+len(prefix):]
	if strings.HasPrefix(rest, `"`) {
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return rest[1:]
		}
		return rest[1 : 1+end]
	}
	if j := strings.IndexAny(rest, " \t"); j >= 0 {
		return rest[:j]
	}
	return rest
}

type hubStatus struct {
	state string // connected | reconnecting | auth-rejected | unknown
	at    string
	msg   string
}

// hubState busca, desde el final del log, el último marcador de conexión al
// hub. El orden de prioridad: rechazo de auth > conectado > reconectando.
func hubState(logPath string) hubStatus {
	if logPath == "" || !fileExists(logPath) {
		return hubStatus{state: "unknown", msg: "no hay log del agente"}
	}

	lines := readTail(logPath, 512)
	res := hubStatus{state: "unknown", msg: "sin actividad registrada aún"}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		ts, _, msg := parseSlogLine(lines[i])
		switch {
		case strings.Contains(msg, "autenticación rechazada"):
			return hubStatus{state: "auth-rejected", at: ts, msg: "el hub rechazó token/printer_id"}
		case strings.Contains(msg, "conectado al hub"), strings.Contains(msg, "handshake enviado"):
			return hubStatus{state: "connected", at: ts, msg: "conectado al hub"}
		case strings.Contains(msg, "conexión perdida"), strings.Contains(msg, "no se pudo conectar"),
			strings.Contains(msg, "reintentando"):
			return hubStatus{state: "reconnecting", at: ts, msg: "reconectando"}
		}
	}
	return res
}

// lastHubTraffic devuelve el timestamp de la última línea del log que muestra
// tráfico con el hub (heartbeat, eventos, comandos, handshake). 0 si no hay.
func lastHubTraffic(logPath string) time.Time {
	if logPath == "" || !fileExists(logPath) {
		return time.Time{}
	}

	markers := []string{
		"evento enviado al hub",
		"comando ejecutado",
		"recibido del hub",
		"handshake enviado",
		"conectado al hub",
		"heartbeat",
	}

	lines := readTail(logPath, 1024)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		for _, m := range markers {
			if !strings.Contains(line, m) {
				continue
			}
			ts, _, _ := parseSlogLine(line)
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				return t
			}
			return time.Now()
		}
	}
	return time.Time{}
}

// readTail devuelve las últimas hasta maxKB de líneas del archivo.
func readTail(path string, maxKB int) []string {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	size := st.Size()
	max := int64(maxKB) * 1024
	offset := int64(0)
	if size > max {
		offset = size - max
	}
	buf := make([]byte, size-offset)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.ReadAt(buf, offset); err != nil && size-offset > 0 {
		return nil
	}
	return strings.Split(string(buf), "\n")
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
