package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/Print-Pilot/printpilot-agent/internal/bridge"
	"github.com/Print-Pilot/printpilot-agent/internal/config"
	"github.com/Print-Pilot/printpilot-agent/internal/moonraker"
	"github.com/Print-Pilot/printpilot-agent/internal/spoolmanproxy"
	"github.com/Print-Pilot/printpilot-agent/internal/tunnel"
	"github.com/Print-Pilot/printpilot-protocol"
)

var version = "dev"

func main() {
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

	// Proxy Spoolman: expone la API compatible con Spoolman para que Moonraker
	// le reporte consumo de filamento, y lo reenvía al hub por el tunnel.
	var proxy *spoolmanproxy.Server
	if cfg.SpoolmanProxyPort > 0 {
		proxy = spoolmanproxy.New(
			log,
			cfg.PrinterID,
			fmt.Sprintf(":%d", cfg.SpoolmanProxyPort),
			moonrakerHTTPBase(cfg.MoonrakerURL),
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

	<-ctx.Done()
	log.Info("señal recibida, cerrando...")
	stop()
	wg.Wait()
	log.Info("adiós")
}

// moonrakerHTTPBase deriva la base HTTP de Moonraker a partir de la URL del
// websocket (ws://host:7125/websocket → http://host:7125). Se usa para
// comunicar el cambio de spool activo vía server.spoolman.spool_id.
func moonrakerHTTPBase(wsURL string) string {
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
