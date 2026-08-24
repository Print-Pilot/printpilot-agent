package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// cmdDoctor replica el chequeo de dependencias del instalador: qué hay en el
// sistema (Klipper/Moonraker requerido, el resto opcional) para diagnosticar
// por qué el agente no rinde.
func cmdDoctor() int {
	fmt.Println()
	fmt.Println("Chequeo de dependencias del sistema")
	fmt.Println()

	type dep struct {
		name string
		ok   bool
		desc string
		opt  bool // opcional (no requerida)
	}

	var deps []dep

	// Klipper (opcional, informativo).
	klipper := false
	kdet := "no encontrado"
	if unitExists("klipper") {
		klipper, kdet = true, "servicio"
	} else {
		for _, p := range []string{"/home/*/klipper/klippy", "/home/*/printer_data/config", "/home/*/printer_data"} {
			if matches := globFirst(p); matches != "" {
				klipper, kdet = true, "en "+matches
				break
			}
		}
	}
	deps = append(deps, dep{"Klipper", klipper, kdet, true})

	// Moonraker (requerido).
	moonraker := false
	mdet := "no encontrado"
	if unitExists("moonraker") {
		moonraker, mdet = true, "servicio"
	} else if online, d := moonrakerProbe("ws://localhost:7125/websocket"); online {
		moonraker, mdet = true, "responde en localhost:7125 ("+d+")"
	}
	deps = append(deps, dep{"Moonraker", moonraker, mdet, false})

	// Crowsnest (opcional, cámara).
	crowsnest := false
	cdet := "no encontrado (opcional)"
	if unitExists("crowsnest") {
		crowsnest, cdet = true, "servicio"
	} else if _, err := exec.LookPath("crowsnest"); err == nil {
		crowsnest, cdet = true, "binario en PATH"
	} else {
		for _, p := range []string{"/home/*/crowsnest/crowsnest", "/home/*/crowsnest/crowsnest.conf",
			"/home/*/printer_data/config/crowsnest.conf", "/usr/local/bin/crowsnest"} {
			if m := globFirst(p); m != "" {
				crowsnest, cdet = true, "en "+m
				break
			}
		}
	}
	deps = append(deps, dep{"Crowsnest", crowsnest, cdet, true})

	// go2rtc (opcional, cámara).
	go2rtc := false
	gdet := "no encontrado (opcional)"
	if _, err := exec.LookPath("go2rtc"); err == nil {
		go2rtc, gdet = true, "binario en PATH"
	} else if unitExists("go2rtc") {
		go2rtc, gdet = true, "servicio"
	}
	deps = append(deps, dep{"go2rtc", go2rtc, gdet, true})

	// systemd (requerido para el servicio).
	systemd := systemdPresent()
	sdet := "no detectado (requerido)"
	if systemd {
		sdet = "disponible"
	}
	deps = append(deps, dep{"systemd", systemd, sdet, false})

	// Descarga (curl/wget) — necesaria para update.
	dl := "no"
	if _, err := exec.LookPath("curl"); err == nil {
		dl = "curl"
	} else if _, err := exec.LookPath("wget"); err == nil {
		dl = "wget"
	}
	deps = append(deps, dep{"Descarga", dl != "no", dl, false})

	for _, d := range deps {
		switch {
		case d.ok:
			fmt.Printf("  [OK]  %-10s %s\n", d.name, d.desc)
		case d.opt:
			fmt.Printf("  [·]   %-10s %s\n", d.name, d.desc)
		default:
			fmt.Printf("  [NO]  %-10s %s\n", d.name, d.desc)
		}
	}

	fmt.Println()

	if !moonraker {
		fmt.Println("Moonraker no está disponible: el agente no va a poder conectarse a la impresora.")
		fmt.Println("Instalalo y reiniciá el agente (sudo systemctl restart printpilot-agent).")
		return 1
	}

	if fileExists(versionFile) {
		fmt.Printf("Agente instalado: %s (versión %s)\n", installDir, installedVersion())
	} else {
		fmt.Printf("El agente no está instalado todavía (falta %s).\n", versionFile)
	}

	return 0
}

// unitExists chequea si una unidad systemd existe (acepta "klipper" o
// "klipper.service"), sin colgarse si systemd está lento.
func unitExists(name string) bool {
	if !systemdPresent() {
		return false
	}
	if fileExists("/etc/systemd/system/"+name+".service") ||
		fileExists("/usr/lib/systemd/system/"+name+".service") ||
		fileExists("/lib/systemd/system/"+name+".service") {
		return true
	}
	out, _ := runOutput("systemctl", "--no-pager", "list-unit-files")
	for _, line := range strings.Split(out, "\n") {
		unit := strings.Fields(line)
		if len(unit) == 0 {
			continue
		}
		if unit[0] == name || unit[0] == name+".service" {
			return true
		}
	}
	return false
}

// globFirst devuelve la primera coincidencia de un glob (o "" si no hay).
func globFirst(pattern string) string {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}
