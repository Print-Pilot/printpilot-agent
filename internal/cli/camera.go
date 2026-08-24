package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Print-Pilot/printpilot-agent/internal/config"
)

const (
	go2rtcBin     = "/usr/local/bin/go2rtc"
	go2rtcConfig  = "/etc/go2rtc.yaml"
	go2rtcUnit    = "/etc/systemd/system/go2rtc.service"
	go2rtcService = "go2rtc.service"
	go2rtcBaseURL = "http://localhost:1984"
)

// cmdCamera gestiona la cámara en vivo (go2rtc + crowsnest):
//
//	printpilot camera setup             instala go2rtc, detecta crowsnest, arma el
//	                                    stream "default" y activa la cámara en el agente
//	printpilot camera setup --stream=<url>   fuerza la URL del stream
//	printpilot camera status            estado de go2rtc y del stream
//	printpilot camera disable           quita go2rtc_url del agente
//	printpilot camera disable --full    además desinstala go2rtc
func cmdCamera(configPath string, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		cameraUsage()
		return 0
	}

	cmd := "setup"
	if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		cmd = args[0]
		args = args[1:]
	}

	streamOverride := ""
	full := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--stream="):
			streamOverride = strings.TrimPrefix(a, "--stream=")
		case a == "--full":
			full = true
		default:
			fmt.Fprintf(os.Stderr, "argumento desconocido: %s\n", a)
			return 2
		}
	}

	switch cmd {
	case "setup":
		return cameraSetup(configPath, streamOverride)
	case "status":
		return cameraStatus()
	case "disable":
		return cameraDisable(configPath, full)
	default:
		cameraUsage()
		return 2
	}
}

func cameraUsage() {
	fmt.Println(`printpilot camera

Uso:
  printpilot camera setup              instala go2rtc, detecta crowsnest y activa la
                                       cámara en el agente (stream "default")
  printpilot camera setup --stream=<url>   fuerza la URL del stream (si no detecta)
  printpilot camera status             estado de go2rtc y del stream
  printpilot camera disable            quita la cámara del agente
  printpilot camera disable --full     además desinstala go2rtc`)
}

// cameraSetup es el instalador "todo en uno" de la cámara.
func cameraSetup(configPath string, streamOverride string) int {
	if !requireRoot() {
		return 1
	}

	// 1) Resolver la URL del stream.
	stream := streamOverride
	transcode := false
	if stream == "" {
		if conf, ok := findCrowsnestConf(); ok {
			if s, t, err := crowsnestStream(conf); err == nil {
				stream = s
				transcode = t
				fmt.Printf("Cámara detectada en %s → stream: %s\n", conf, stream)
			} else {
				fmt.Printf("Aviso: no pude interpretar crowsnest.conf (%v)\n", err)
			}
		} else {
			fmt.Println("Aviso: no encontré crowsnest.conf (buscando en ~/printer_data/config y /home/*/crowsnest).")
		}
	}
	if stream == "" {
		stream = promptStream()
		// Sin detección no sabemos el codec; asumir MJPG (transcode) es lo
		// seguro para cámaras web de Klipper.
		transcode = true
	}
	if stream == "" {
		fmt.Fprintln(os.Stderr, "No se definió el stream. Usá: printpilot camera setup --stream=<url>")
		return 1
	}

	// go2rtc v1.9.14 no auto-transcodeca MJPG: envolver con ffmpeg:.
	source := go2rtcStreamUrl(stream, transcode)
	if source != stream {
		fmt.Printf("Transcode habilitado (MJPG → H264 vía ffmpeg): %s\n", source)
	}

	// 2) Instalar go2rtc.
	if err := installGo2rtc(); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo instalar go2rtc: %v\n", err)
		return 1
	}
	fmt.Println("go2rtc instalado.")

	// 3) ffmpeg: necesario para servir MJPG (ustreamer) por WebRTC. Sin él el
	// WHEP de go2rtc falla con HTTP 500 EOF al crear el peer.
	if err := ensureFFmpeg(); err != nil {
		fmt.Fprintf(os.Stderr, "Aviso: %v\n", err)
		fmt.Fprintln(os.Stderr, "El WHEP puede fallar si la cámara es MJPG y no hay ffmpeg.")
	}

	// 4) Config + servicio.
	if err := writeGo2rtcConfig(source); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo escribir %s: %v\n", go2rtcConfig, err)
		return 1
	}
	if err := enableGo2rtc(); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo arrancar go2rtc: %v\n", err)
		return 1
	}
	fmt.Println("Servicio go2rtc activo.")

	// 4) Activar la cámara en el agente.
	if err := setAgentGo2rtc(configPath, go2rtcBaseURL); err != nil {
		fmt.Fprintf(os.Stderr, "Aviso: no se pudo setear go2rtc_url en el agente: %v\n", err)
		return 1
	}

	// 5) Verificar.
	time.Sleep(2 * time.Second)
	return cameraStatus()
}

// findCrowsnestConf ubica el crowsnest.conf de la máquina.
func findCrowsnestConf() (string, bool) {
	patterns := []string{
		"/home/*/printer_data/config/crowsnest.conf",
		"/home/*/crowsnest/crowsnest.conf",
		"/etc/crowsnest.conf",
	}
	for _, p := range patterns {
		if m, err := filepath.Glob(p); err == nil && len(m) > 0 {
			return m[0], true
		}
	}
	return "", false
}

// crowsnestStream parsea la primer cámara del crowsnest.conf y deriva la URL.
// El segundo retorno indica si el source necesita transcode (MJPG → WebRTC):
// ustreamer emite JPEG, que WebRTC no lleva → go2rtc debe pasar por ffmpeg.
func crowsnestStream(confPath string) (string, bool, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return "", false, err
	}

	inCam := false
	camName := ""
	fields := map[string]string{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if inCam {
				break // ya capturamos la primera cámara
			}
			sec := strings.Trim(line, "[]")
			if strings.HasPrefix(strings.ToLower(sec), "cam") {
				inCam = true
				camName = strings.TrimSpace(strings.TrimPrefix(sec, "cam"))
			}
			continue
		}
		if !inCam {
			continue
		}
		if i := strings.Index(line, ":"); i > 0 {
			val := strings.TrimSpace(line[i+1:])
			// crowsnest permite comentarios al final de la línea.
			if j := strings.Index(val, "#"); j >= 0 {
				val = strings.TrimSpace(val[:j])
			}
			fields[strings.TrimSpace(line[:i])] = val
		}
	}
	if !inCam {
		return "", false, fmt.Errorf("no hay ninguna sección [cam ...] en %s", confPath)
	}

	mode := strings.ToLower(fields["mode"])
	port := fields["port"]
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		name = camName
	}

	switch {
	case strings.Contains(mode, "v4l2"):
		if port == "" {
			port = "8554"
		}
		if name == "" || name == "1" {
			name = "stream"
		}
		return "rtsp://127.0.0.1:" + port + "/" + name, false, nil
	case strings.Contains(mode, "camera-streamer"):
		if port == "" {
			port = "8554"
		}
		return "rtsp://127.0.0.1:" + port + "/stream", false, nil
	case strings.Contains(mode, "ustreamer"):
		if port == "" {
			port = "8080"
		}
		return "http://127.0.0.1:" + port + "/?action=stream", true, nil
	default:
		if port == "" {
			port = "8080"
		}
		return "http://127.0.0.1:" + port + "/?action=stream", true, nil
	}
}

// go2rtcStreamUrl envuelve el source con ffmpeg si hace falta transcode
// (MJPG → H264). go2rtc NO auto-transcodeca en v1.9.14: sin el prefijo
// ffmpeg: devuelve "codecs not matched: video:JPEG => video:H264".
func go2rtcStreamUrl(url string, transcode bool) string {
	if transcode {
		return "ffmpeg:" + url + "#video=h264"
	}
	return url
}

func promptStream() string {
	fmt.Print("Pegá la URL del stream de la cámara (ej. rtsp://127.0.0.1:8554/webcam): ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// installGo2rtc descarga el binario de go2rtc para esta arquitectura.
func installGo2rtc() error {
	if fileExists(go2rtcBin) {
		return nil
	}

	url := "https://github.com/AlexxIT/go2rtc/releases/latest/download/go2rtc_linux_" + go2rtcArch()
	fmt.Printf("Descargando go2rtc (%s)...\n", url)
	tmp := go2rtcBin + ".tmp"
	if err := downloadFile(url, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, go2rtcBin); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(go2rtcBin, 0o755); err != nil {
		return err
	}

	return nil
}

// go2rtcArch mapea GOARCH al nombre del asset de go2rtc (386 → i386).
func go2rtcArch() string {
	switch runtime.GOARCH {
	case "386":
		return "i386"
	default:
		return runtime.GOARCH
	}
}

// ensureFFmpeg verifica que ffmpeg exista y, si no, lo instala con apt-get.
// go2rtc lo necesita para transcodecar MJPG (ustreamer) a H264/VP8 para WebRTC.
func ensureFFmpeg() error {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return fmt.Errorf("ffmpeg no está instalado y no hay apt-get para instalarlo")
	}

	fmt.Println("ffmpeg no está instalado (necesario para MJPG → WebRTC). Instalando...")
	update := exec.Command("apt-get", "update", "-qq")
	update.Stdout, update.Stderr = os.Stdout, os.Stderr
	_ = update.Run()

	install := exec.Command("apt-get", "install", "-y", "-qq", "ffmpeg")
	install.Stdout, install.Stderr = os.Stdout, os.Stderr
	if err := install.Run(); err != nil {
		return fmt.Errorf("no se pudo instalar ffmpeg con apt-get: %w", err)
	}

	fmt.Println("ffmpeg instalado.")
	return nil
}

func writeGo2rtcConfig(stream string) error {
	conf := fmt.Sprintf("streams:\n  default: %s\nwebrtc:\n  ice_servers:\n    - urls: [stun:stun.l.google.com:19302]\n", stream)
	return os.WriteFile(go2rtcConfig, []byte(conf), 0o644)
}

const go2rtcUnitContent = `[Unit]
Description=go2rtc (PrintPilot camera)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/go2rtc -config /etc/go2rtc.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func enableGo2rtc() error {
	if err := os.WriteFile(go2rtcUnit, []byte(go2rtcUnitContent), 0o644); err != nil {
		return err
	}
	if _, err := runOutput("systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, _ = runOutput("systemctl", "enable", go2rtcService)
	_, err := runOutput("systemctl", "restart", go2rtcService)
	return err
}

// setAgentGo2rtc activa go2rtc_url en la config del agente y reinicia.
func setAgentGo2rtc(configPath, url string) error {
	if !fileExists(configPath) {
		return fmt.Errorf("no existe %s", configPath)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}
	cfg.Go2rtcURL = url
	if err := cfg.Save(configPath); err != nil {
		return err
	}
	restartAfterChange()
	return nil
}

// cameraStatus muestra el estado de go2rtc y del stream.
func cameraStatus() int {
	fmt.Println()
	fmt.Println("Cámara (go2rtc)")

	if !fileExists(go2rtcBin) {
		fmt.Println("  go2rtc:          no instalado")
		fmt.Println("  → printpilot camera setup")
		return 1
	}
	fmt.Printf("  binario:         %s\n", go2rtcBin)
	fmt.Printf("  config:          %s\n", go2rtcConfig)

	if systemdPresent() {
		fmt.Printf("  servicio:        %s\n", go2rtcServiceActive())
	}

	streams := go2rtcStreams()
	if streams == "" {
		fmt.Println("  go2rtc:          no responde en " + go2rtcBaseURL)
		return 1
	}
	fmt.Println("  streams:")
	for _, line := range strings.Split(strings.TrimSpace(streams), "\n") {
		fmt.Println("    " + line)
	}
	fmt.Println()

	if !strings.Contains(strings.ToLower(streams), "error") {
		fmt.Println("El stream 'default' está listo. Abrí el panel → impresora → Cámara.")
		return 0
	}
	fmt.Println("El stream reporta error: verificá la URL de la cámara en " + go2rtcConfig)
	fmt.Println("  (corregila y: sudo systemctl restart go2rtc)")

	return 0
}

func go2rtcServiceActive() string {
	out, _ := runOutput("systemctl", "is-active", go2rtcService)
	if out == "" {
		return "inactivo"
	}
	return out
}

// go2rtcStreams consulta la API de go2rtc y devuelve un resumen por stream.
func go2rtcStreams() string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(go2rtcBaseURL + "/api/streams")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var streams map[string]struct {
		State string `json:"state"`
		Ready bool   `json:"ready"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&streams); err != nil {
		return ""
	}

	var b strings.Builder
	for name, s := range streams {
		state := s.State
		if state == "" {
			state = "unknown"
		}
		fmt.Fprintf(&b, "%s: %s\n", name, state)
	}
	return b.String()
}

// cameraDisable quita la cámara del agente (y opcionalmente desinstala go2rtc).
func cameraDisable(configPath string, full bool) int {
	if !requireRoot() {
		return 1
	}

	if fileExists(configPath) {
		if cfg, err := config.LoadConfig(configPath); err == nil && cfg.Go2rtcURL != "" {
			cfg.Go2rtcURL = ""
			if err := cfg.Save(configPath); err == nil {
				fmt.Println("go2rtc_url quitado del agente.")
				restartAfterChange()
			} else {
				fmt.Fprintf(os.Stderr, "No se pudo guardar la config: %v\n", err)
				return 1
			}
		} else {
			fmt.Println("go2rtc_url no estaba configurado.")
		}
	}

	if full {
		if systemdPresent() {
			_, _ = runOutput("systemctl", "stop", go2rtcService)
			_, _ = runOutput("systemctl", "disable", go2rtcService)
		}
		_ = os.Remove(go2rtcUnit)
		_ = os.Remove(go2rtcConfig)
		_ = os.Remove(go2rtcBin)
		fmt.Println("go2rtc desinstalado.")
	} else {
		fmt.Println("Para desinstalar go2rtc del sistema: printpilot camera disable --full")
	}

	return 0
}
