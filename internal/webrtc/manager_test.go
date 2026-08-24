package webrtc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Print-Pilot/printpilot-protocol"
)

type fakeClient struct {
	createErr    error
	createCalls  int
	lastCamera   string
	lastOfferSDP string
}

func (f *fakeClient) CreatePeer(ctx context.Context, camera, offer string) (string, error) {
	f.createCalls++
	f.lastCamera = camera
	f.lastOfferSDP = offer
	if f.createErr != nil {
		return "", f.createErr
	}
	return "answer-" + camera, nil
}

type captureSend struct {
	envs []*protocol.Envelope
}

func (c *captureSend) send(ctx context.Context, env *protocol.Envelope) error {
	c.envs = append(c.envs, env)
	return nil
}

func newTestManager(fake *fakeClient) (*Manager, *captureSend) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := &captureSend{}
	m := NewManager(log, "ender3", 15*time.Second, "", fake, cs.send)
	return m, cs
}

func TestOfferHappyPath(t *testing.T) {
	fake := &fakeClient{}
	m, cs := newTestManager(fake)
	ctx := context.Background()

	m.dispatch(ctx, event{kind: evOffer, msg: protocol.WebrtcOffer{
		WebrtcBase: protocol.WebrtcBase{SessionID: "s-1", CameraID: "default"},
		SDP:        "offer-sdp",
	}})

	if fake.createCalls != 1 {
		t.Fatalf("CreatePeer llamada %d veces, esperaba 1", fake.createCalls)
	}
	if len(cs.envs) != 1 || cs.envs[0].Type != protocol.TypeWebrtcAnswer {
		t.Fatalf("se esperaba un answer, got %d envs", len(cs.envs))
	}
	var ans protocol.WebrtcAnswer
	if err := cs.envs[0].UnmarshalPayload(&ans); err != nil {
		t.Fatalf("answer inválido: %v", err)
	}
	if ans.SDP != "answer-default" || ans.SessionID != "s-1" {
		t.Fatalf("answer inesperado: %+v", ans)
	}
	if s := m.sess["s-1"]; s == nil || s.state != stateConnected {
		t.Fatalf("la sesión debería estar connected")
	}
}

func TestOfferErrorSendsSessionEnd(t *testing.T) {
	fake := &fakeClient{createErr: errors.New("boom")}
	m, cs := newTestManager(fake)

	m.dispatch(context.Background(), event{kind: evOffer, msg: protocol.WebrtcOffer{
		WebrtcBase: protocol.WebrtcBase{SessionID: "s-2"},
		SDP:        "offer",
	}})

	if len(cs.envs) != 1 || cs.envs[0].Type != protocol.TypeWebrtcSessionEnd {
		t.Fatalf("se esperaba un session_end de error, got %d envs", len(cs.envs))
	}
	var end protocol.WebrtcSessionEnd
	if err := cs.envs[0].UnmarshalPayload(&end); err != nil {
		t.Fatalf("session_end inválido: %v", err)
	}
	if end.Reason != protocol.WebrtcSessionEndError {
		t.Fatalf("reason = %q, esperaba %q", end.Reason, protocol.WebrtcSessionEndError)
	}
	if _, ok := m.sess["s-2"]; ok {
		t.Fatalf("la sesión fallida no debería persistir")
	}
}

// TestIceCandidateIgnored: en el camino go2rtc (non-trickle) los candidates se
// descartan sin llamar a go2rtc (no hay endpoint de trickle).
func TestIceCandidateIgnored(t *testing.T) {
	fake := &fakeClient{}
	m, _ := newTestManager(fake)

	m.dispatch(context.Background(), event{kind: evIceCandidate, msg: protocol.WebrtcIceCandidate{
		WebrtcBase: protocol.WebrtcBase{SessionID: "ghost"},
		Candidate:  "cand",
	}})
	if fake.createCalls != 0 {
		t.Fatalf("no se debió llamar a go2rtc por un ice_candidate")
	}
}

// TestSessionEndDropsRecord: el cierre solo elimina el registro; go2rtc se
// limpia solo al cerrarse el peer del navegador (no hay DELETE WHEP).
func TestSessionEndDropsRecord(t *testing.T) {
	fake := &fakeClient{}
	m, _ := newTestManager(fake)
	ctx := context.Background()

	m.dispatch(ctx, event{kind: evOffer, msg: protocol.WebrtcOffer{
		WebrtcBase: protocol.WebrtcBase{SessionID: "s-3"},
		SDP:        "offer",
	}})
	if _, ok := m.sess["s-3"]; !ok {
		t.Fatalf("la sesión debería existir tras el offer")
	}

	m.dispatch(ctx, event{kind: evSessionEnd, msg: protocol.WebrtcSessionEnd{
		WebrtcBase: protocol.WebrtcBase{SessionID: "s-3"},
		Reason:     protocol.WebrtcSessionEndUserClosed,
	}})

	if _, ok := m.sess["s-3"]; ok {
		t.Fatalf("la sesión cerrada no debería persistir")
	}
}

// TestCameraStreamResolution: si la config define camera_stream, se usa como
// stream de go2rtc sin importar el camera_id del offer (MVP single-camera).
func TestCameraStreamResolution(t *testing.T) {
	fake := &fakeClient{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := &captureSend{}
	m := NewManager(log, "ender3", 15*time.Second, "camara1", fake, cs.send)
	ctx := context.Background()

	m.dispatch(ctx, event{kind: evOffer, msg: protocol.WebrtcOffer{
		WebrtcBase: protocol.WebrtcBase{SessionID: "s-1", CameraID: "default"},
		SDP:        "offer",
	}})

	if fake.lastCamera != "camara1" {
		t.Fatalf("CreatePeer src = %q, esperaba %q (camera_stream)", fake.lastCamera, "camara1")
	}
	if fake.lastOfferSDP != "offer" {
		t.Fatalf("el SDP del offer debe pasar tal cual, got %q", fake.lastOfferSDP)
	}
}

// TestCameraStreamDefault: sin camera_stream, el camera_id del offer ES el
// nombre del stream de go2rtc.
func TestCameraStreamDefault(t *testing.T) {
	fake := &fakeClient{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := &captureSend{}
	m := NewManager(log, "ender3", 15*time.Second, "", fake, cs.send)
	ctx := context.Background()

	m.dispatch(ctx, event{kind: evOffer, msg: protocol.WebrtcOffer{
		WebrtcBase: protocol.WebrtcBase{SessionID: "s-1", CameraID: "test"},
		SDP:        "offer",
	}})

	if fake.lastCamera != "test" {
		t.Fatalf("CreatePeer src = %q, esperaba %q (camera_id)", fake.lastCamera, "test")
	}
}

// TestNegotiationTimeoutExpires verifica el sweep sobre sesiones que nunca
// completaron la negociación: se cierran y se avisa con reason timeout.
func TestNegotiationTimeoutExpires(t *testing.T) {
	fake := &fakeClient{}
	m, cs := newTestManager(fake)
	ctx := context.Background()

	m.sess["s-stuck"] = &session{
		id:        "s-stuck",
		camera:    "default",
		state:     stateNegotiating,
		createdAt: time.Now().Add(-30 * time.Second), // > negTimeout (15s)
	}

	m.sweep(ctx)

	if _, ok := m.sess["s-stuck"]; ok {
		t.Fatalf("la sesión en negotiating expirada debería eliminarse")
	}
	if len(cs.envs) != 1 || cs.envs[0].Type != protocol.TypeWebrtcSessionEnd {
		t.Fatalf("se esperaba session_end timeout, got %d envs", len(cs.envs))
	}
	var end protocol.WebrtcSessionEnd
	if err := cs.envs[0].UnmarshalPayload(&end); err != nil {
		t.Fatalf("session_end inválido: %v", err)
	}
	if end.Reason != protocol.WebrtcSessionEndTimeout {
		t.Fatalf("reason = %q, esperaba timeout", end.Reason)
	}
}

// TestSweepNoFalseKillOnConnectedSession: una sesión conectada jamás se mata
// por falta de actividad (la pestaña puede estar minimizada sin generar
// señalización; la conexión RTC sigue viva).
func TestSweepNoFalseKillOnConnectedSession(t *testing.T) {
	fake := &fakeClient{}
	m, _ := newTestManager(fake)
	ctx := context.Background()

	m.sess["s-idle"] = &session{
		id:        "s-idle",
		camera:    "default",
		state:     stateConnected,
		createdAt: time.Now().Add(-10 * time.Minute), // vieja, sin actividad
	}

	m.sweep(ctx)

	if _, ok := m.sess["s-idle"]; !ok {
		t.Fatalf("una sesión conectada no debe ser barrida por silencio")
	}
}

// TestTeardownClearsSessions: al apagar el agente se limpia el estado.
func TestTeardownClearsSessions(t *testing.T) {
	fake := &fakeClient{}
	m, _ := newTestManager(fake)

	m.sess["a"] = &session{id: "a", camera: "default", state: stateConnected}
	m.sess["b"] = &session{id: "b", camera: "default", state: stateConnected}

	m.teardown()

	if len(m.sess) != 0 {
		t.Fatalf("el mapa de sesiones debería quedar vacío")
	}
}
