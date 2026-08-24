package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// cmdUpdate descarga la última versión del binario para esta arquitectura y
// reinicia el servicio. Equivale a `install.sh update`.
func cmdUpdate() int {
	if !requireRoot() {
		return 1
	}
	if !fileExists(installDir + "/printpilot-agent") {
		fmt.Fprintln(os.Stderr, "El agente no está instalado. Usá el instalador (install.sh).")
		return 1
	}

	current := installedVersion()
	target, err := resolveLatestVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo consultar la última versión: %v\n", err)
		fmt.Fprintln(os.Stderr, "Revisá la conexión a api.github.com o el rate-limit de GitHub.")
		return 1
	}

	fmt.Printf("Actualizando: %s → %s\n", current, target)
	if current == target {
		fmt.Println("Ya tenés la última versión. Nada que hacer.")
		return 0
	}

	arch := detectArch()
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/printpilot-agent-linux-%s", repo, target, arch)

	tmp := installDir + "/printpilot-agent.tmp"
	if err := downloadFile(url, tmp); err != nil {
		os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "No se pudo descargar %s: %v\n", url, err)
		return 1
	}

	// Verificación opcional de checksum.
	verifyChecksum(url, tmp)

	if err := os.Rename(tmp, installDir+"/printpilot-agent"); err != nil {
		os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "No se pudo reemplazar el binario: %v\n", err)
		return 1
	}
	if err := os.Chmod(installDir+"/printpilot-agent", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Aviso: no se pudo ajustar permisos: %v\n", err)
	}
	_ = os.WriteFile(versionFile, []byte(target+"\n"), 0o644)

	if err := restartService(); err != nil {
		fmt.Fprintf(os.Stderr, "Aviso: actualizado a %s pero no se pudo reiniciar el servicio (%v)\n", target, err)
		return 0
	}
	fmt.Printf("Actualizado a %s y servicio reiniciado.\n", target)

	return 0
}

// resolveLatestVersion consulta la última release en GitHub.
func resolveLatestVersion() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api.github.com respondió HTTP %d", resp.StatusCode)
	}

	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("la release no trae tag_name")
	}
	return body.TagName, nil
}

func detectArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "386":
		return "386"
	case "arm":
		return "arm"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// verifyChecksum intenta bajar el .sha256 del release; si falla, avisa y sigue.
func verifyChecksum(url, file string) {
	resp, err := http.Get(url + ".sha256")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		fmt.Println("Aviso: no se pudo verificar el checksum (se omite).")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	expected := strings.Fields(string(body))
	if len(expected) == 0 {
		return
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != expected[0] {
		fmt.Fprintln(os.Stderr, "¡Checksum no coincide con la release publicada!")
		os.Remove(file)
	}
}
