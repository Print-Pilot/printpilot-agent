package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/Print-Pilot/printpilot-agent/internal/config"
)

// cmdPrinter gestiona el printer_id de ESTA máquina (una impresora por agente):
//
//	printpilot printer new [nombre]  genera printer_id + token, los persiste y
//	                                 reinicia (los dos se pegan en el panel)
//	printpilot printer show          muestra printer_id y token actuales
//	printpilot printer set <id>      fija un printer_id explícito
func cmdPrinter(configPath string, args []string) int {
	if !requireRoot() {
		return 1
	}

	if len(args) == 0 {
		printerUsage()
		return 2
	}

	switch args[0] {
	case "new":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return printerNew(configPath, name)
	case "show":
		return printerShow(configPath)
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "uso: printpilot printer set <printer_id>")
			return 2
		}
		return printerSet(configPath, args[1])
	case "-h", "--help", "help":
		printerUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %s\n", args[0])
		printerUsage()
		return 2
	}
}

func printerUsage() {
	fmt.Println(`printpilot printer

Uso:
  printpilot printer new [nombre]  crea la impresora y devuelve el printer_id
                                   y el token (persistidos en la config + reinicia)
  printpilot printer show          muestra el printer_id y el token actuales
  printpilot printer set <id>      fija un printer_id explícito

El printer_id y el token que devuelve se pegan en el panel (Printers → Crear
impresora) para asociar la impresora a este agente.`)
}

func printerNew(configPath string, name string) int {
	if !requireRoot() {
		return 1
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo leer la config: %v\n", err)
		return 1
	}

	base := name
	if base == "" {
		base, _ = os.Hostname()
	}
	id := newPrinterID(base)
	token := randomToken(40)

	cfg.PrinterID = id
	cfg.Token = token
	if err := cfg.Save(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo guardar la config: %v\n", err)
		return 1
	}

	fmt.Println()
	fmt.Println("  Impresora creada:")
	fmt.Printf("    printer_id:  %s\n", id)
	fmt.Printf("    token:       %s\n", token)
	fmt.Println()
	fmt.Println("  En el panel: Printers → Crear impresora, pegá estos dos valores")
	fmt.Println("  (printer_id y token) y guardá.")
	fmt.Println()

	restartAfterChange()

	return 0
}

func printerShow(configPath string) int {
	if !requireRoot() {
		return 1
	}

	if !fileExists(configPath) {
		fmt.Fprintln(os.Stderr, "No hay config. Corré el instalador o usa 'printpilot printer new'.")
		return 1
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo leer la config: %v\n", err)
		return 1
	}

	fmt.Printf("printer_id:  %s\n", cfg.PrinterID)
	if cfg.Token == "" {
		fmt.Println("token:       (vacío — usá 'printpilot printer new' para generar id y token)")
	} else {
		fmt.Printf("token:       %s\n", cfg.Token)
	}
	if cfg.PrinterID == "" {
		fmt.Println("(printer_id vacío — usá 'printpilot printer new' para generar uno)")
	}

	return 0
}

func printerSet(configPath string, id string) int {
	if !requireRoot() {
		return 1
	}

	id = strings.TrimSpace(id)
	if id == "" {
		fmt.Fprintln(os.Stderr, "printer_id vacío")
		return 2
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo leer la config: %v\n", err)
		return 1
	}
	cfg.PrinterID = id
	if err := cfg.Save(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo guardar la config: %v\n", err)
		return 1
	}

	fmt.Printf("printer_id fijado: %s\n", id)
	restartAfterChange()

	return 0
}

// newPrinterID genera un printer_id legible a partir de un nombre (o hostname):
//
//	"Taller 2" → taller-2-9f3a2b
func newPrinterID(name string) string {
	return slugify(name) + "-" + randomSuffix(3)
}

// restartAfterChange reinicia el servicio si está instalado; si no, avisa que
// hay que reiniciar el agente manualmente para que tome la nueva config.
func restartAfterChange() {
	if !systemdPresent() || !serviceUnitExists() {
		fmt.Println("(no hay servicio systemd: reiniciá el agente manualmente para aplicar el cambio)")
		return
	}
	if err := restartService(); err != nil {
		fmt.Fprintf(os.Stderr, "Aviso: no se pudo reiniciar el servicio (%v)\n", err)
		return
	}
	fmt.Println("Servicio reiniciado. El cambio ya está aplicado.")
}
