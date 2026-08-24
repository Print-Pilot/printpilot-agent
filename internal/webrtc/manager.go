package webrtc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Print-Pilot/printpilot-protocol"
)

// sweepInterval es la frecuencia del barrido de sesiones.
const sweepInterval = 15 * time.Second

// queueCap es la capacidad del buffer de eventos de señalización. Si se llena
// se descartan mensajes (la señalización es efímera y el read-loop del tunnel
// jamás debe frenarse por WebRTC).
const queueCap = 128

type sessionState int

const (
	stateNegotiating sessionState = iota // esperando answer de go2rtc
	stateConnected                       // answer generado y enviado al panel
)

// session es el estado de una negociación puntual (correlacionada por session_id).
// Nota: go2rtc no expone handle de sesión en el camino WHEP (validado v1.9.14);
// el peer se limpia solo al cerrarse la conexión del navegador. Por eso el
// manager no guarda location ni hace DELETE: solo correlaciona y controla el
// timeout de negociación.
type session struct {
	id        string
	camera    string
	state     sessionState
	createdAt time.Time
}

type eventKind int

const (
	evOffer eventKind = iota
	evIceCandidate
	evSessionEnd
)

type event struct {
	kind eventKind
	msg  any
}

// Manager orquesta la señalización WebRTC del Agent. Todos los mensajes del hub
// (webrtc.offer / webrtc.ice_candidate / webrtc.session_end) se encolan y se
// procesan en una única goroutine FIFO (Run): se preserva el orden por sesión
// (offer → answer) y no hay carrera de datos. Los handlers de encolado son
// O(1) y nunca bloquean el read-loop del tunnel.
//
// Política de "actividad" (ver PLAN.md): las sesiones CONECTADAS jamás se
// matan por silencio (minimizar la pestaña no genera señalización, pero la
// conexión RTC sigue viva). Se cierran por session_end explícito o por timeout
// de negociación. La detección de peer muerto la hace go2rtc (se limpia solo).
//
// Trickle ICE: go2rtc no lo soporta por HTTP (PATCH 405). Los candidates del
// navegador viajan DENTRO del offer (non-trickle), así que los webrtc.ice_candidate
// que lleguen se ignoran. Los candidates de go2rtc viajan en el answer.
type Manager struct {
	log          *slog.Logger
	printerID    string
	negTimeout   time.Duration
	cameraStream string // stream de go2rtc a servir; vacío = usar el camera_id del offer
	client       Go2rtc
	send         func(context.Context, *protocol.Envelope) error

	queue chan event
	sess  map[string]*session
}

// NewManager crea el manager. send se usa para enviar envelopes al hub (el
// bridge le pasa tun.SendEnvelope). cameraStream es el stream de go2rtc que se
// sirve para camera_id "default" (single-camera MVP); vacío usa el camera_id.
func NewManager(log *slog.Logger, printerID string, negTimeout time.Duration, cameraStream string, client Go2rtc, send func(context.Context, *protocol.Envelope) error) *Manager {
	return &Manager{
		log:          log,
		printerID:    printerID,
		negTimeout:   negTimeout,
		cameraStream: cameraStream,
		client:       client,
		send:         send,
		queue:        make(chan event, queueCap),
		sess:         make(map[string]*session),
	}
}

// HandleOffer encola un webrtc.offer del hub. No bloquea: si la cola está
// llena, descarta el mensaje y loguea.
func (m *Manager) HandleOffer(msg protocol.WebrtcOffer) {
	m.enqueue(evOffer, msg)
}

// HandleIceCandidate encola un webrtc.ice_candidate del hub. En el camino
// go2rtc (non-trickle) se descartan: los candidates ya viajan en el offer.
func (m *Manager) HandleIceCandidate(msg protocol.WebrtcIceCandidate) {
	m.enqueue(evIceCandidate, msg)
}

// HandleSessionEnd encola un webrtc.session_end del hub.
func (m *Manager) HandleSessionEnd(msg protocol.WebrtcSessionEnd) {
	m.enqueue(evSessionEnd, msg)
}

func (m *Manager) enqueue(kind eventKind, msg any) {
	select {
	case m.queue <- event{kind: kind, msg: msg}:
	default:
		m.log.Warn("webrtc: cola de señalización llena, descartando mensaje", "kind", kind)
	}
}

// Run procesa la señalización hasta que ctx se cancela. Bloqueante.
func (m *Manager) Run(ctx context.Context) {
	m.log.Info("webrtc manager iniciado", "go2rtc", m.client != nil, "neg_timeout", m.negTimeout)

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.teardown()
			return
		case ev := <-m.queue:
			m.dispatch(ctx, ev)
		case <-ticker.C:
			m.sweep(ctx)
		}
	}
}

func (m *Manager) dispatch(ctx context.Context, ev event) {
	switch ev.kind {
	case evOffer:
		m.handleOffer(ctx, ev.msg.(protocol.WebrtcOffer))
	case evIceCandidate:
		// Non-trickle: los candidates ya van dentro del offer. Descartar.
		m.log.Debug("webrtc: ice_candidate ignorado (go2rtc no soporta trickle HTTP)", "session_id", ev.msg.(protocol.WebrtcIceCandidate).SessionID)
	case evSessionEnd:
		m.handleSessionEnd(ctx, ev.msg.(protocol.WebrtcSessionEnd))
	}
}

// handleOffer pide el answer a go2rtc y lo devuelve al panel. Si falla, avisa
// con un session_end reason error.
func (m *Manager) handleOffer(ctx context.Context, offer protocol.WebrtcOffer) {
	s := &session{
		id:        offer.SessionID,
		camera:    offer.Camera(),
		state:     stateNegotiating,
		createdAt: time.Now(),
	}
	if _, exists := m.sess[s.id]; exists {
		m.log.Warn("webrtc: offer para sesión ya existente, ignorando", "session_id", s.id)
		return
	}
	m.sess[s.id] = s

	// Diagnóstico: longitud del SDP que llega del navegador. Un 0 aquí
	// confirma que el offer se perdió/truncó en la cadena panel→hub→agente.
	m.log.Info("webrtc: offer recibido", "session_id", s.id, "camera", s.camera, "sdp_len", len(offer.SDP))

	// Resolver el stream de go2rtc: si hay camera_stream configurado se usa para
	// cualquier camera_id (single-camera MVP); si no, el camera_id del offer ES
	// el nombre del stream.
	src := m.cameraStream
	if src == "" {
		src = s.camera
	}

	answer, err := m.client.CreatePeer(ctx, src, offer.SDP)
	if err != nil {
		m.log.Warn("webrtc: no se pudo crear peer en go2rtc", "session_id", s.id, "camera", s.camera, "error", err)
		m.sendSessionEnd(ctx, s.id, protocol.WebrtcSessionEndError)
		delete(m.sess, s.id)
		return
	}

	s.state = stateConnected
	m.log.Info("webrtc: peer creado en go2rtc", "session_id", s.id, "camera", s.camera)

	// El answer viaja por el mismo camino (agent → hub → panel → navegador).
	m.sendAnswer(ctx, s.id, s.camera, answer)
}

// handleSessionEnd cierra el registro de la sesión. El peer en go2rtc ya se
// cerró (o se cerrará solo) porque el navegador cerró su conexión RTC al
// mandar el session_end: no hay DELETE disponible en el camino WHEP.
func (m *Manager) handleSessionEnd(ctx context.Context, end protocol.WebrtcSessionEnd) {
	if _, ok := m.sess[end.SessionID]; !ok {
		return
	}
	m.log.Info("webrtc: sesión cerrada", "session_id", end.SessionID, "reason", end.Reason)
	delete(m.sess, end.SessionID)
}

// sweep barre sesiones en negotiating que pasaron el timeout de negociación
// (nunca nacieron): se cierran y se avisa al panel con reason timeout.
func (m *Manager) sweep(ctx context.Context) {
	now := time.Now()
	for id, s := range m.sess {
		if s.state == stateNegotiating && m.negTimeout > 0 && now.Sub(s.createdAt) > m.negTimeout {
			m.log.Warn("webrtc: sesión expiró en negociación", "session_id", id)
			m.sendSessionEnd(ctx, id, protocol.WebrtcSessionEndTimeout)
			delete(m.sess, id)
		}
	}
}

// teardown limpia el estado al apagar el agente. No hay nada que liberar en
// go2rtc (se limpia solo al cerrarse los peers).
func (m *Manager) teardown() {
	m.log.Info("webrtc: apagando, limpiando sesiones", "activas", len(m.sess))
	m.sess = make(map[string]*session)
}

// sendAnswer envía un webrtc.answer al hub.
func (m *Manager) sendAnswer(ctx context.Context, sessionID, camera, sdp string) {
	m.sendMessage(ctx, protocol.TypeWebrtcAnswer, protocol.WebrtcAnswer{
		WebrtcBase: protocol.WebrtcBase{SessionID: sessionID, CameraID: camera},
		SDP:        sdp,
	})
}

// sendSessionEnd envía un webrtc.session_end al hub.
func (m *Manager) sendSessionEnd(ctx context.Context, sessionID, reason string) {
	m.sendMessage(ctx, protocol.TypeWebrtcSessionEnd, protocol.WebrtcSessionEnd{
		WebrtcBase: protocol.WebrtcBase{SessionID: sessionID},
		Reason:     reason,
	})
}

func (m *Manager) sendMessage(ctx context.Context, msgType string, payload any) {
	if m.send == nil {
		return
	}
	env, err := protocol.NewEnvelope(msgType, m.printerID, payload)
	if err != nil {
		m.log.Warn("webrtc: no se pudo armar mensaje al hub", "type", msgType, "error", err)
		return
	}
	if err := m.send(ctx, env); err != nil {
		m.log.Debug("webrtc: no se pudo enviar mensaje al hub", "type", msgType, "error", err)
	}
}
