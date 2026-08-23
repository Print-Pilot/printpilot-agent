package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Print-Pilot/printpilot-agent/internal/backoff"
)

// ErrAuthRejected se devuelve (envuelto) cuando una conexión fue rechazada por
// un motivo permanente (ej. token inválido), para que Run no reintente en loop
// rápido sino con esperas largas.
var ErrAuthRejected = errors.New("auth rechazada")

// ConnectFn establece la conexión y devuelve una función de cierre y un
// handler que corre hasta que la conexión se cae (devuelve un error) o el
// contexto se cancela.
type ConnectFn func(ctx context.Context) (cleanup func(), run func(ctx context.Context) error, err error)

// Run mantiene la conexión viva: conecta, corre el handler, y ante un error
// espera con backoff y reintenta. Devuelve solo cuando el contexto se cancela.
// Si el error es de autenticación rechazada, usa la espera máxima fija en vez
// de un backoff agresivo, para no saturar el hub con un token inválido.
func Run(ctx context.Context, log *slog.Logger, name string, bo backoff.Config, connect ConnectFn) {
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cleanup, run, err := connect(ctx)
		if err != nil {
			log.Warn("no se pudo conectar", "target", name, "error", err)
		} else {
			attempt = 0
			log.Info("conectado", "target", name)

			runErr := safeRun(run, ctx, log, name)
			if cleanup != nil {
				cleanup()
			}
			if ctx.Err() != nil {
				return
			}
			if runErr != nil {
				if errors.Is(runErr, ErrAuthRejected) {
					log.Error("autenticación rechazada por el hub; revisar token/printer_id. Reintentando cada 60s",
						"target", name)
					if !sleep(ctx, time.Minute) {
						return
					}
					continue
				}
				log.Warn("conexión perdida", "target", name, "error", runErr)
			}
		}

		wait := bo.Next(attempt)
		attempt++
		log.Info("reintentando", "target", name, "attempt", attempt, "in", wait.Round(time.Millisecond))

		if !sleep(ctx, wait) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// safeRun ejecuta el handler convirtiendo cualquier panic en un error manejado,
// como red de seguridad para que un bug puntual no tumbe todo el proceso.
func safeRun(run func(ctx context.Context) error, ctx context.Context, log *slog.Logger, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic en handler de %s: %v", name, r)
			log.Error("recuperado de panic", "target", name, "panic", r)
		}
	}()
	return run(ctx)
}
