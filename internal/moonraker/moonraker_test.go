package moonraker

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func TestApplyPartialKeepsStateOnPartialUpdates(t *testing.T) {
	c := New("ws://localhost:7125/websocket", testLogger())

	// Estado inicial: printing.
	apply(c, t, map[string]string{
		"print_stats": `{"state":"printing","filename":"x.gcode","print_duration":10}`,
	})
	if c.status.State != "printing" {
		t.Fatalf("state = %q, want printing", c.status.State)
	}

	// Parcial SIN state (solo progreso): no debe borrar el estado.
	apply(c, t, map[string]string{
		"print_stats": `{"print_duration":13}`,
	})
	if c.status.State != "printing" {
		t.Fatalf("el parcial sin state pisó el estado: %q", c.status.State)
	}

	// Parcial sin print_stats en absoluto: tampoco.
	apply(c, t, map[string]string{
		"extruder": `{"temperature":210}`,
	})
	if c.status.State != "printing" {
		t.Fatalf("parcial sin print_stats pisó el estado: %q", c.status.State)
	}

	// Transición real a complete.
	apply(c, t, map[string]string{
		"print_stats": `{"state":"complete"}`,
	})
	if c.status.State != "complete" {
		t.Fatalf("state = %q, want complete", c.status.State)
	}
}

// apply arma un partial con print_stats y llama applyPartial (bajo lock).
func apply(c *Client, t *testing.T, m map[string]string) {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range m {
		raw[k] = json.RawMessage(v)
	}
	c.applyPartial(raw)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
