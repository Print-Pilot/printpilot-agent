// Package spoolmanproxy implementa un servidor HTTP+WS local compatible con la
// API de Spoolman, suficiente para que Moonraker le reporte consumo de filamento.
//
// Moonraker se conecta a este servidor (config `[spoolman] server:`) y, cada
// sync_rate segundos, le manda `PUT /api/v1/spool/{id}/use {"use_length": mm}`.
// El proxy convierte ese reporte en un protocol.FilamentUsageReport y lo
// reenvía al hub por el tunnel. No guarda inventario: solo traduce y reenvía.
//
// Ver SPOOLMAN_COMPAT.md para el contrato exacto que Moonraker espera.
package spoolmanproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Print-Pilot/printpilot-protocol"
)

// maxPendingReports limita el buffer en memoria de reportes no enviados cuando
// el tunnel está caído, para no acumular memoria indefinidamente.
const maxPendingReports = 1000

// Sender es lo que el proxy necesita para mandar envelopes al hub.
type Sender interface {
	SendEnvelope(ctx context.Context, env *protocol.Envelope) error
}

// Server es el proxy Spoolman local.
type Server struct {
	log            *slog.Logger
	printerID      string
	addr           string
	moonrakerBase  string // base URL HTTP de Moonraker (ej. http://localhost:7125)
	sender         Sender
	wsConn         *websocket.Conn
	wsMu           sync.Mutex
	mu             sync.Mutex
	pending        []protocol.FilamentUsageReport
	spoolExists    func(spoolID int64) bool // para GET /spool/{id}; nil = siempre 200
}

// Option configura el Server.
type Option func(*Server)

// WithSpoolExists establece el chequeo de existencia de spool para GET /spool/{id}.
// Si no se setea, GET devuelve 200 siempre (Moonraker solo usa el status code).
func WithSpoolExists(fn func(spoolID int64) bool) Option {
	return func(s *Server) { s.spoolExists = fn }
}

// New crea un proxy Spoolman en addr (ej. ":8001") que reenvía a sender.
// moonrakerBase es la base HTTP de Moonraker para SetActiveSpool.
func New(log *slog.Logger, printerID, addr, moonrakerBase string, sender Sender, opts ...Option) *Server {
	s := &Server{
		log:           log,
		printerID:     printerID,
		addr:          addr,
		moonrakerBase: strings.TrimRight(moonrakerBase, "/"),
		sender:        sender,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Run inicia el servidor HTTP+WS y bloquea hasta que ctx se cancele.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/spool", s.handleSpoolWS) // WS
	mux.HandleFunc("/api/v1/spool/", s.handleSpoolHTTP)

	srv := &http.Server{Addr: s.addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("proxy spoolman escuchando", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// handleSpoolWS acepta la conexión WS que Moonraker abre y la mantiene viva.
// Moonraker solo la usa para recibir eventos; el proxy no necesita enviar nada
// salvo responder a los pings (lo maneja coder/websocket automáticamente).
func (s *Server) handleSpoolWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("no se pudo aceptar WS spoolman", "error", err)
		return
	}
	s.wsMu.Lock()
	s.wsConn = conn
	s.wsMu.Unlock()
	defer func() {
		s.wsMu.Lock()
		if s.wsConn == conn {
			s.wsConn = nil
		}
		s.wsMu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "proxy cerrando")
	}()

	s.log.Info("Moonraker conectado al proxy spoolman (WS)")
	for {
		// Leer (y descartar) mensajes entrantes; responder pings lo maneja
		// coder/websocket. Si el cliente cierra, se termina el loop.
		if _, _, err := conn.Read(r.Context()); err != nil {
			s.log.Info("WS spoolman desconectado", "error", err)
			return
		}
	}
}

// handleSpoolHTTP maneja GET /api/v1/spool/{id} y PUT /api/v1/spool/{id}/use.
func (s *Server) handleSpoolHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// parts: ["api","v1","spool","{id}",("use")?]
	if len(parts) < 4 || parts[2] != "spool" {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	spoolID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid spool id")
		return
	}

	isUse := len(parts) >= 5 && parts[4] == "use"

	switch {
	case !isUse && r.Method == http.MethodGet:
		s.handleGetSpool(w, spoolID)
	case isUse && r.Method == http.MethodPut:
		s.handleUseSpool(w, r, spoolID)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleGetSpool(w http.ResponseWriter, spoolID int64) {
	if s.spoolExists != nil && !s.spoolExists(spoolID) {
		s.writeError(w, http.StatusNotFound, "spool not found")
		return
	}
	// Moonraker solo usa el status code; devolvemos un Spool mínimo.
	s.writeJSON(w, http.StatusOK, map[string]any{"id": spoolID})
}

func (s *Server) handleUseSpool(w http.ResponseWriter, r *http.Request, spoolID int64) {
	var body struct {
		UseLength *float64 `json:"use_length"`
		UseWeight *float64 `json:"use_weight"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// Moonraker manda use_length (mm). Lo reenviamos al hub como reporte.
	// Si no hay densidad para convertir mm→g, reenviamos length y el panel decide.
	rep := protocol.FilamentUsageReport{
		SpoolID:   &spoolID,
		Timestamp: time.Now().Unix(),
	}
	if body.UseWeight != nil {
		rep.GramsUsed = *body.UseWeight
	}
	if body.UseLength != nil {
		rep.LengthMM = *body.UseLength
	}
	// Si no tenemos gramos, mandamos 0 y la longitud; el panel puede convertir.
	s.queueReport(rep)

	// Responder con el Spool actualizado (Moonraker solo usa el status code).
	s.writeJSON(w, http.StatusOK, map[string]any{"id": spoolID})
}

// queueReport acumula el reporte en un buffer acotado. Si el tunnel está caído,
// no se pierde el dato: se reenvía en el próximo flush.
func (s *Server) queueReport(rep protocol.FilamentUsageReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) >= maxPendingReports {
		s.log.Warn("buffer de reportes spoolman lleno, descartando reporte más viejo")
		s.pending = s.pending[1:]
	}
	s.pending = append(s.pending, rep)
}

// Flush intenta enviar todos los reportes pendientes al hub. Devuelve cuántos
// quedaron sin enviar. Se llama periódicamente y tras reconexión del tunnel.
func (s *Server) Flush(ctx context.Context) int {
	s.mu.Lock()
	reports := s.pending
	s.pending = nil
	s.mu.Unlock()

	var remaining int
	for _, rep := range reports {
		env, err := protocol.NewEnvelope(protocol.TypeFilamentUsage, s.printerID, rep)
		if err != nil {
			s.log.Error("armar filament usage report", "error", err)
			continue
		}
		if err := s.sender.SendEnvelope(ctx, env); err != nil {
			// No se pudo enviar: lo devolvemos al buffer para reintentar.
			s.mu.Lock()
			s.pending = append([]protocol.FilamentUsageReport{rep}, s.pending...)
			s.mu.Unlock()
			remaining++
			continue
		}
		s.log.Debug("filament usage report enviado al hub",
			"spool_id", rep.SpoolID, "grams", rep.GramsUsed, "length_mm", rep.LengthMM)
	}
	return remaining
}

// PendingCount devuelve cuántos reportes hay esperando en el buffer.
func (s *Server) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// SetActiveSpool recibe un SetActiveSpool del hub y lo reenvía a Moonraker vía
// HTTP (POST /server/spoolman/spool_id), luego envía el ack por el tunnel.
func (s *Server) SetActiveSpool(ctx context.Context, req protocol.SetActiveSpool) {
	ack := protocol.SetActiveSpoolAck{RequestID: req.RequestID, Success: false}

	url := fmt.Sprintf("%s/server/spoolman/spool_id", s.moonrakerBase)
	body, _ := json.Marshal(map[string]any{"spool_id": req.SpoolID})
	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		ack.Error = "no se pudo contactar a Moonraker: " + err.Error()
		s.sendSpoolAck(ctx, ack)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		ack.Success = true
		s.log.Info("spool activo seteado en Moonraker", "spool_id", req.SpoolID)
	} else {
		ack.Error = fmt.Sprintf("Moonraker respondió %d", resp.StatusCode)
	}
	s.sendSpoolAck(ctx, ack)
}

func (s *Server) sendSpoolAck(ctx context.Context, ack protocol.SetActiveSpoolAck) {
	env, err := protocol.NewEnvelope(protocol.TypeSetActiveSpoolAck, s.printerID, ack)
	if err != nil {
		s.log.Error("armar set_active_spool_ack", "error", err)
		return
	}
	if err := s.sender.SendEnvelope(ctx, env); err != nil {
		s.log.Warn("enviar set_active_spool_ack", "error", err)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]any{"message": msg})
}
