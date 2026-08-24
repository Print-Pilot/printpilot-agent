package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/Print-Pilot/printpilot-agent/internal/bridge"
	"github.com/Print-Pilot/printpilot-agent/internal/cli"
	"github.com/Print-Pilot/printpilot-agent/internal/config"
	"github.com/Print-Pilot/printpilot-agent/internal/moonraker"
	"github.com/Print-Pilot/printpilot-agent/internal/spoolmanproxy"
	"github.com/Print-Pilot/printpilot-agent/internal/tunnel"
	"github.com/Print-Pilot/printpilot-agent/internal/webrtc"
	"github.com/Print-Pilot/printpilot-protocol"
)

var version = "dev"

func main() {
	// Modo CLI: el mismo binario es un multi-llamada. Si se invoca como
	// `printpilot` (symlink que crea el instalador) o el primer argumento es
	// un subcomando conocido, actuamos como CLI de gestión. Si no, como daemon.
	if isCLIInvocation() {
		cli.Version = version
		os.Exit(cli.Run(os.Args[1:]))
	}

	configPath := flag.String("config", "agent-config.yaml", "ruta del archivo de configuración")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("no se pudo cargar la configuración", "error", err)
		os.Exit(1)
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	log.Info("config cargada",
		"hub_url", cfg.HubURL,
		"moonraker_url", cfg.MoonrakerURL,
		"printer_id", cfg.PrinterID,
		"log_file", cfg.LogFile,
		"version", version,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	objects := []string{"print_stats", "extruder", "heater_bed"}

	moons := moonraker.New(cfg.MoonrakerURL, log)
	tun := tunnel.New(cfg.HubURL, cfg.PrinterID, cfg.Token, version, log)

	br := bridge.New(cfg.PrinterID, moons, tun, log)
	br.Start()

	// WebRTC (cámara en vivo): el agente es un intermediario transparente de
	// señalización entre el hub y go2rtc (WHEP). El video fluye P2P navegador
	// <-> go2rtc. Los handlers encolan y vuelven al toque (no frenan el
	// read-loop del tunnel); el manager los procesa en una goroutine FIFO.
	var webrtcMgr *webrtc.Manager
	if cfg.Go2rtcURL != "" {
		client := webrtc.NewClient(cfg.Go2rtcURL)
		webrtcMgr = webrtc.NewManager(log, cfg.PrinterID, cfg.WebrtcTimeout, cfg.CameraStream, client, tun.SendEnvelope)
		tun.SetWebrtcOfferCallback(webrtcMgr.HandleOffer)
		tun.SetWebrtcIceCandidateCallback(webrtcMgr.HandleIceCandidate)
		tun.SetWebrtcSessionEndCallback(webrtcMgr.HandleSessionEnd)
	} else {
		log.Info("streaming de cámara deshabilitado (falta go2rtc_url)")
	}

	// Proxy Spoolman: expone la API compatible con Spoolman para que Moonraker
	// le reporte consumo de filamento, y lo reenvía al hub por el tunnel.
	var proxy *spoolmanproxy.Server
	if cfg.SpoolmanProxyPort > 0 {
		proxy = spoolmanproxy.New(
			log,
			cfg.PrinterID,
			fmt.Sprintf(":%d", cfg.SpoolmanProxyPort),
			cli.MoonrakerHTTPBase(cfg.MoonrakerURL),
			tun,
		)
		// SetActiveSpool del hub → Moonraker.
		tun.SetActiveSpoolCallback(func(req protocol.SetActiveSpool) {
			proxy.SetActiveSpool(context.Background(), req)
		})
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		moons.Run(ctx, objects)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tun.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		br.HeartbeatLoop(ctx, cfg.HeartbeatInterval)
	}()

	if proxy != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = proxy.Run(ctx)
		}()

		// Flush periódico: reintenta enviar reportes pendientes (tunnel caído).
		wg.Add(1)
		go func() {
			defer wg.Done()
			flushTicker := time.NewTicker(3 * time.Second)
			defer flushTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-flushTicker.C:
					if remaining := proxy.Flush(ctx); remaining > 0 {
						log.Warn("reportes spoolman pendientes de envío", "pendientes", remaining)
					}
				}
			}
		}()
	}

	if webrtcMgr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			webrtcMgr.Run(ctx)
		}()
	}

	<-ctx.Done()
	log.Info("señal recibida, cerrando...")
	stop()
	wg.Wait()
	log.Info("adiós")
}

// isCLIInvocation decide entre modo CLI y modo daemon:
//   - invocado como `printpilot` (symlink creado por install.sh) → CLI
//   - primer argumento es un subcomando conocido → CLI
//   - cualquier otra cosa (p. ej. `printpilot-agent -config ...` de systemd) → daemon
func isCLIInvocation() bool {
	if strings.EqualFold(filepath.Base(os.Args[0]), "printpilot") {
		return true
	}
	return len(os.Args) > 1 && cli.IsSubcommand(os.Args[1])
}

// newLogger construye el logger: a stdout siempre, y a un archivo rotable
// (lumberjack) si log_file está configurado. Respeta log_level.
func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}

	var writer io.Writer = os.Stdout
	if cfg.LogFile != "" {
		writer = io.MultiWriter(os.Stdout, &lumberjack.Logger{
			Filename:   cfg.LogFile,
			MaxSize:    10, // MB
			MaxBackups: 5,
			MaxAge:     28, // días
			Compress:   true,
		})
	}

	return slog.New(slog.NewTextHandler(writer, opts))
}
