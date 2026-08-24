package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Print-Pilot/printpilot-agent/internal/config"
)

// cmdLog muestra el log del agente: `printpilot log [n]` (últimas n líneas,
// default 30) o `printpilot log -f` (sigue en vivo, como tail -f).
func cmdLog(configPath string, args []string) int {
	n := 30
	follow := false

	for _, a := range args {
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case a == "-h" || a == "--help":
			fmt.Println("uso: printpilot log [-f] [n]")
			return 0
		default:
			if v, err := strconv.Atoi(a); err == nil && v > 0 {
				n = v
			} else {
				fmt.Fprintf(os.Stderr, "argumento inválido: %s\n", a)
				return 2
			}
		}
	}

	logPath := resolveLogPath(configPath)
	if logPath == "" {
		fmt.Fprintln(os.Stderr, "No se pudo determinar el log (config sin log_file).")
		return 1
	}
	if !fileExists(logPath) {
		fmt.Fprintf(os.Stderr, "No existe el log: %s\n", logPath)
		return 1
	}

	if follow {
		// tail -f está en cualquier sistema Linux/macOS.
		cmd := exec.Command("tail", "-n", "0", "-f", logPath)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			return 1
		}
		return 0
	}

	tailLog(logPath, n)
	return 0
}

// resolveLogPath obtiene el log_file de la config (o el default del sistema).
func resolveLogPath(configPath string) string {
	if fileExists(configPath) {
		if cfg, err := config.LoadConfig(configPath); err == nil && cfg.LogFile != "" {
			return cfg.LogFile
		}
	}
	return defaultLogDir + "/agent.log"
}

// tailLog imprime las últimas n líneas del archivo.
func tailLog(path string, n int) {
	lines := readTail(path, 1024)
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	for _, l := range lines[start:] {
		if strings.TrimSpace(l) != "" {
			fmt.Println(l)
		}
	}
}
