package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Print-Pilot/printpilot-agent/internal/backoff"
)

func TestRunReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var connects atomic.Int32
	fast := backoff.Config{Initial: 5 * time.Millisecond, Max: 5 * time.Millisecond, Factor: 1.0}

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", fast,
			func(ctx context.Context) (func(), func(context.Context) error, error) {
				connects.Add(1)
				n := connects.Load()
				// primer intento: connect falla → reintenta
				if n == 1 {
					return nil, nil, errors.New("connect falló")
				}
				// siguientes: run devuelve error simulando caída → reintenta
				return func() {}, func(context.Context) error {
					return errors.New("conexión caída")
				}, nil
			})
	}()

	// esperamos que reconecte al menos 3 veces
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if connects.Load() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := connects.Load(); got < 3 {
		t.Fatalf("se esperaba al menos 3 intentos de conexión, se hicieron %d", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no retornó tras cancelar el contexto")
	}
}
