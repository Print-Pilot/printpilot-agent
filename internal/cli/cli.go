// Package cli implementa la interfaz de línea de comandos de printpilot-agent:
// el mismo binario actúa como daemon (cuando lo corre systemd) o como CLI de
// gestión cuando se invoca como `printpilot` (symlink) o con un subcomando.
//
// Comandos:
//
//	printpilot status               estado del servicio, config y conectividad
//	printpilot doctor               chequeo de dependencias del sistema
//	printpilot printer new [nombre] crea una impresora y devuelve su printer_id
//	printpilot printer show         muestra el printer_id actual
//	printpilot printer set <id>     fija un printer_id explícito
//	printpilot config get [clave]   muestra la config
//	printpilot config set <clave> <valor>  cambia un valor y reinicia el servicio
//	printpilot log [-f] [n]         muestra el log del agente (follow con -f)
//	printpilot update               actualiza el binario a la última versión
//	printpilot uninstall            desinstala el agente (respaldando la config)
//	printpilot version              versión instalada
package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/Print-Pilot/printpilot-agent/internal/config"
)

// Version es la versión de build (inyectada por main vía ldflags).
var Version = "dev"

const (
	serviceName   = "printpilot-agent.service"
	installDir    = "/opt/printpilot-agent"
	configDir     = "/etc/printpilot-agent"
	defaultConfig = configDir + "/config.yaml"
	versionFile   = installDir + "/VERSION"
	runUser       = "printpilot"
	defaultLogDir = "/var/log/printpilot-agent"
	cliLink       = "/usr/local/bin/printpilot"
	repo          = "Print-Pilot/printpilot-agent"
)

var subcommands = map[string]bool{
	"status": true, "doctor": true, "printer": true, "config": true,
	"log": true, "update": true, "uninstall": true, "version": true,
	"help": true,
}

// IsSubcommand indica si el argumento es un subcomando del CLI. Se usa en main
// para decidir entre modo daemon y modo CLI.
func IsSubcommand(s string) bool { return subcommands[s] }

// Run despacha el CLI y devuelve el código de salida.
func Run(args []string) int {
	configPath := defaultConfig
	rest := args
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		switch {
		case rest[0] == "-config" || rest[0] == "--config":
			if len(rest) < 2 {
				fmt.Fprintln(os.Stderr, "falta el valor de -config")
				return 2
			}
			configPath = rest[1]
			rest = rest[2:]
		default:
			fmt.Fprintf(os.Stderr, "flag desconocido: %s\n", rest[0])
			return 2
		}
	}

	if env := os.Getenv("PRINTPILOT_CONFIG"); env != "" {
		configPath = env
	}
	// Si el config del sistema no existe, caer al de desarrollo (misma máquina).
	if configPath == defaultConfig && !fileExists(configPath) {
		if fileExists("agent-config.yaml") {
			configPath = "agent-config.yaml"
		}
	}

	if len(rest) == 0 {
		usage()
		return 2
	}

	switch rest[0] {
	case "status":
		return cmdStatus(configPath)
	case "doctor":
		return cmdDoctor()
	case "printer":
		return cmdPrinter(configPath, rest[1:])
	case "config":
		return cmdConfig(configPath, rest[1:])
	case "log":
		return cmdLog(configPath, rest[1:])
	case "update":
		return cmdUpdate()
	case "uninstall":
		return cmdUninstall()
	case "version":
		return cmdVersion()
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "comando desconocido: %s\n", rest[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Println(`printpilot — CLI de gestión de printpilot-agent

Uso:
  printpilot <comando> [argumentos]

Comandos:
  status                estado del servicio, config y conectividad
  doctor                chequeo de dependencias del sistema
  printer new [nombre]  crea una impresora y devuelve el printer_id
  printer show          muestra el printer_id actual
  printer set <id>      fija un printer_id explícito
  config get [clave]    muestra la configuración (token enmascarado)
  config set <clave> <valor>  cambia un valor y reinicia el servicio
  log [-f] [n]          muestra el log del agente (n líneas, -f para seguir)
  update                actualiza el binario a la última versión
  uninstall             desinstala el agente (respaldando la config)
  version               versión instalada

Flags globales:
  -config <ruta>        ruta del config (por defecto /etc/printpilot-agent/config.yaml)

Ejemplos:
  printpilot status
  printpilot printer new taller
  printpilot config set moonraker_url ws://localhost:7125/websocket`)
}

// --- Helpers ----------------------------------------------------------------

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isRoot() bool { return os.Geteuid() == 0 }

// requireRoot re-ejecuta el comando con sudo si hace falta. Devuelve false si
// el comando ya fue manejado en un proceso hijo (no seguir en el padre).
func requireRoot() bool {
	if isRoot() {
		return true
	}
	if _, err := exec.LookPath("sudo"); err == nil {
		cmd := exec.Command("sudo", os.Args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			os.Exit(1)
		}
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "Este comando requiere permisos de root (corré con sudo).")
	return false
}

func runOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func systemdPresent() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func serviceUnitExists() bool {
	return fileExists("/etc/systemd/system/" + serviceName)
}

func serviceState() string {
	if !systemdPresent() {
		return ""
	}
	out, _ := runOutput("systemctl", "is-active", serviceName)
	return out
}

func serviceEnabled() string {
	if !systemdPresent() {
		return ""
	}
	out, _ := runOutput("systemctl", "is-enabled", serviceName)
	return out
}

func restartService() error {
	if !systemdPresent() {
		return fmt.Errorf("systemd no está disponible en este sistema")
	}
	_, err := runOutput("systemctl", "restart", serviceName)
	return err
}

func installedVersion() string {
	b, err := os.ReadFile(versionFile)
	if err != nil {
		return Version
	}
	return strings.TrimSpace(string(b))
}

func loadCLIConfig(path string) (*config.Config, error) {
	return config.LoadConfig(path)
}

// MoonrakerHTTPBase deriva la base HTTP de Moonraker a partir de su websocket
// (ws://host:7125/websocket → http://host:7125).
func MoonrakerHTTPBase(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil {
		return strings.TrimSuffix(wsURL, "/websocket")
	}
	scheme := u.Scheme
	switch scheme {
	case "wss":
		scheme = "https"
	case "ws":
		scheme = "http"
	}
	return scheme + "://" + u.Host
}

// --- Utilidades de output ---------------------------------------------------

// maskToken muestra un token enmascarado (primeros 6 chars + ***).
func maskToken(t string) string {
	if t == "" {
		return "(vacío)"
	}
	if len(t) <= 6 {
		return strings.Repeat("*", len(t))
	}
	return t[:6] + strings.Repeat("*", len(t)-6)
}

func randomSuffix(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		// Fallback determinista: no debería pasar.
		return strings.Repeat("0", nBytes*2)
	}
	return hex.EncodeToString(b)
}

const tokenCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomToken genera un token alfanumérico de n caracteres (mismo formato que
// el Str::random(40) del panel, para que ambos lados usen el mismo alfabeto).
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return randomSuffix(n) // hex, también alfanumérico
	}
	for i := range b {
		b[i] = tokenCharset[int(b[i])%len(tokenCharset)]
	}
	return string(b)
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "printer"
	}
	return out
}
