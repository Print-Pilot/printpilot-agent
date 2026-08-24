package cli

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// cmdUninstall detiene y remueve el servicio, respalda la config y borra los
// directorios/el usuario del agente. Equivale a `install.sh delete`.
func cmdUninstall() int {
	if !requireRoot() {
		return 1
	}

	if !serviceUnitExists() && !fileExists(installDir) {
		fmt.Println("No hay nada instalado (no existe el servicio ni /opt/printpilot-agent).")
		return 0
	}

	fmt.Println("Deteniendo y removiendo el servicio")
	if systemdPresent() {
		_, _ = runOutput("systemctl", "stop", serviceName)
		_, _ = runOutput("systemctl", "disable", serviceName)
		_ = os.Remove("/etc/systemd/system/" + serviceName)
		_, _ = runOutput("systemctl", "daemon-reload")
	}

	// Respaldo de la config (nunca borrar token/URLs sin copia).
	if fileExists(configDir + "/config.yaml") {
		backup := fmt.Sprintf("%s/config.yaml.bak.%d", configDir, time.Now().Unix())
		if err := copyFile(configDir+"/config.yaml", backup); err == nil {
			fmt.Printf("Config respaldada en %s\n", backup)
		}
	}

	// Symlink del CLI.
	_ = os.Remove(cliLink)

	_ = os.RemoveAll(installDir)
	_ = os.RemoveAll(configDir)
	_ = os.RemoveAll(defaultLogDir)

	if _, err := exec.LookPath("userdel"); err == nil {
		_, _ = runOutput("userdel", runUser)
	}

	fmt.Println("printpilot-agent desinstalado.")
	fmt.Println("La config quedó respaldada (config.yaml.bak.*).")
	return 0
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
