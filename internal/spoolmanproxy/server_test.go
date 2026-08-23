package spoolmanproxy

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Print-Pilot/printpilot-protocol"
)

// fakeSender captura los envelopes enviados al hub.
type fakeSender struct {
	envelopes chan *protocol.Envelope
}

func (f *fakeSender) SendEnvelope(_ context.Context, env *protocol.Envelope) error {
	f.envelopes <- env
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func TestUseSpoolQueuesAndFlushes(t *testing.T) {
	sender := &fakeSender{envelopes: make(chan *protocol.Envelope, 10)}
	srv := New(discardLogger(), "ender3", ":0", "http://moonraker:7125", sender)

	// Simular Moonraker mandando PUT /api/v1/spool/7/use
	body := `{"use_length": 356.7}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/spool/7/use", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleSpoolHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperaba 200", rec.Code)
	}

	if got := srv.PendingCount(); got != 1 {
		t.Fatalf("PendingCount = %d, esperaba 1", got)
	}

	// Flush envía el reporte al hub
	srv.Flush(context.Background())

	select {
	case env := <-sender.envelopes:
		if env.Type != protocol.TypeFilamentUsage {
			t.Fatalf("type = %q, esperaba %q", env.Type, protocol.TypeFilamentUsage)
		}
		var rep protocol.FilamentUsageReport
		if err := env.UnmarshalPayload(&rep); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if rep.SpoolID == nil || *rep.SpoolID != 7 {
			t.Errorf("spool_id = %v, esperaba 7", rep.SpoolID)
		}
		if rep.LengthMM != 356.7 {
			t.Errorf("length_mm = %v, esperaba 356.7", rep.LengthMM)
		}
	default:
		t.Fatal("no se envió ningún envelope al hub")
	}

	if got := srv.PendingCount(); got != 0 {
		t.Fatalf("PendingCount tras flush = %d, esperaba 0", got)
	}
}

func TestGetSpoolReturns404WhenMissing(t *testing.T) {
	sender := &fakeSender{envelopes: make(chan *protocol.Envelope, 10)}
	// Simula que el spool 999 no existe
	srv := New(discardLogger(), "ender3", ":0", "http://moonraker:7125", sender,
		WithSpoolExists(func(id int64) bool { return id != 999 }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/spool/999", nil)
	rec := httptest.NewRecorder()
	srv.handleSpoolHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperaba 404", rec.Code)
	}

	reqOK := httptest.NewRequest(http.MethodGet, "/api/v1/spool/7", nil)
	recOK := httptest.NewRecorder()
	srv.handleSpoolHTTP(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("status = %d, esperaba 200", recOK.Code)
	}
}

func TestBufferRetainsOnFlushFailure(t *testing.T) {
	// Sender que siempre falla (tunnel caído)
	failing := &fakeSenderFail{}
	srv := New(discardLogger(), "ender3", ":0", "http://moonraker:7125", failing)

	body := `{"use_length": 10}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/spool/1/use", strings.NewReader(body))
	srv.handleSpoolHTTP(httptest.NewRecorder(), req)

	if got := srv.PendingCount(); got != 1 {
		t.Fatalf("PendingCount = %d, esperaba 1", got)
	}

	// Flush falla: el reporte vuelve al buffer
	srv.Flush(context.Background())
	if got := srv.PendingCount(); got != 1 {
		t.Fatalf("PendingCount tras flush fallido = %d, esperaba 1 (retry)", got)
	}
}

type fakeSenderFail struct{}

func (f *fakeSenderFail) SendEnvelope(_ context.Context, _ *protocol.Envelope) error {
	return errTunnelDown
}

type errString string

func (e errString) Error() string { return string(e) }

const errTunnelDown = errString("tunnel caído")
