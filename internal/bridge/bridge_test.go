package bridge

import (
	"context"
	"log/slog"
	"io"
	"sync"
	"testing"

	"github.com/Print-Pilot/printpilot-agent/internal/moonraker"
	"github.com/Print-Pilot/printpilot-protocol"
)

// mockTunnel captura los envelopes que el bridge envía al hub.
type mockTunnel struct {
	mu      sync.Mutex
	sent    []*protocol.Envelope
	command func(protocol.Command)
}

func (m *mockTunnel) SendEnvelope(_ context.Context, env *protocol.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, env)
	return nil
}

func (m *mockTunnel) SetCommandCallback(fn func(protocol.Command)) {
	m.command = fn
}

func (m *mockTunnel) SetFileListRequestCallback(fn func(protocol.FileListRequest)) {}

func (m *mockTunnel) SetFileGetRequestCallback(fn func(protocol.FileGetRequest)) {}

func (m *mockTunnel) SetFileUploadRequestCallback(fn func(protocol.FileUploadRequest)) {}

func (m *mockTunnel) SetFileDeleteRequestCallback(fn func(protocol.FileDeleteRequest)) {}

func (m *mockTunnel) SetFileMkdirRequestCallback(fn func(protocol.FileMkdirRequest)) {}

func (m *mockTunnel) SetFileMoveRequestCallback(fn func(protocol.FileMoveRequest)) {}

func (m *mockTunnel) events() []*protocol.Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*protocol.Envelope
	for _, e := range m.sent {
		if e.Type == protocol.TypeEvent {
			out = append(out, e)
		}
	}
	return out
}

func newTestBridge(t *testing.T) (*Bridge, *mockTunnel) {
	t.Helper()
	tun := &mockTunnel{}
	// moon no se usa en la detección de eventos; pasamos nil.
	b := New("ender3-test", nil, tun, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return b, tun
}

// feed simula un status update de Moonraker con el estado dado.
func feed(b *Bridge, state, filename string, duration float64) {
	b.onMoonrakerStatus(moonraker.Status{
		State:         state,
		Filename:      filename,
		PrintDuration: duration,
	})
}

func TestPrintStartedOnTransition(t *testing.T) {
	b, tun := newTestBridge(t)

	feed(b, "standby", "", 0)   // inicial
	feed(b, "printing", "benchy.gcode", 1.0) // arranca

	events := tun.events()
	if len(events) != 1 {
		t.Fatalf("se esperaba 1 evento print_started, se enviaron %d", len(events))
	}
	var ev protocol.Event
	if err := events[0].UnmarshalPayload(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.EventType != protocol.EventPrintStarted {
		t.Fatalf("EventType = %q, esperaba %q", ev.EventType, protocol.EventPrintStarted)
	}
	if ev.Metadata["filename"] != "benchy.gcode" {
		t.Fatalf("filename = %q, esperaba benchy.gcode", ev.Metadata["filename"])
	}
}

func TestPrintFinishedOnComplete(t *testing.T) {
	b, tun := newTestBridge(t)

	feed(b, "standby", "", 0)                // inicial (no activo)
	feed(b, "printing", "benchy.gcode", 10)  // print_started
	feed(b, "complete", "benchy.gcode", 120.5) // print_finished

	events := tun.events()
	if len(events) != 2 {
		t.Fatalf("se esperaban 2 eventos (started+finished), se enviaron %d", len(events))
	}
	var ev protocol.Event
	_ = events[1].UnmarshalPayload(&ev)
	if ev.EventType != protocol.EventPrintFinished {
		t.Fatalf("EventType = %q, esperaba %q", ev.EventType, protocol.EventPrintFinished)
	}
	if ev.Metadata["print_duration"] != "120.5" {
		t.Fatalf("print_duration = %q, esperaba 120.5", ev.Metadata["print_duration"])
	}
}

func TestPrintFailedOnError(t *testing.T) {
	b, tun := newTestBridge(t)

	feed(b, "standby", "", 0)              // inicial
	feed(b, "printing", "benchy.gcode", 10) // print_started
	feed(b, "error", "benchy.gcode", 10)    // print_failed

	events := tun.events()
	if len(events) != 2 {
		t.Fatalf("se esperaban 2 eventos (started+failed), se enviaron %d", len(events))
	}
	var ev protocol.Event
	_ = events[1].UnmarshalPayload(&ev)
	if ev.EventType != protocol.EventPrintFailed {
		t.Fatalf("EventType = %q, esperaba %q", ev.EventType, protocol.EventPrintFailed)
	}
}

func TestNoEventOnUnrelatedTransition(t *testing.T) {
	b, tun := newTestBridge(t)

	feed(b, "standby", "", 0)
	feed(b, "ready", "", 0)

	if got := len(tun.events()); got != 0 {
		t.Fatalf("no se esperaba ningún evento, se enviaron %d", got)
	}
}
