package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/coder/websocket"

	"github.com/Print-Pilot/printpilot-agent/internal/backoff"
	"github.com/Print-Pilot/printpilot-agent/internal/runner"
	"github.com/Print-Pilot/printpilot-protocol"
)

// CommandFn recibe cada comando enviado por el hub.
type CommandFn func(protocol.Command)
type FileListRequestFn func(protocol.FileListRequest)
type FileGetRequestFn func(protocol.FileGetRequest)
type FileUploadRequestFn func(protocol.FileUploadRequest)
type FileDeleteRequestFn func(protocol.FileDeleteRequest)
type FileMkdirRequestFn func(protocol.FileMkdirRequest)
type FileMoveRequestFn func(protocol.FileMoveRequest)
type SetActiveSpoolFn func(protocol.SetActiveSpool)

// WebrtcOfferFn recibe cada offer de señalización WebRTC (cámara en vivo) del
// hub. El handler NO debe bloquear: debe encolar y volver al toque, para no
// frenar el read-loop del tunnel (telemetría/comandos).
type WebrtcOfferFn func(protocol.WebrtcOffer)
type WebrtcIceCandidateFn func(protocol.WebrtcIceCandidate)
type WebrtcSessionEndFn func(protocol.WebrtcSessionEnd)

type Client struct {
	url           string
	printerID     string
	token         string
	agentVer      string
	log           *slog.Logger
	conn          *websocket.Conn
	ready         bool // solo se activa tras enviar el handshake
	writeMu       sync.Mutex
	onCommand     CommandFn
	onFileList    FileListRequestFn
	onFileGet     FileGetRequestFn
	onFileUpload  FileUploadRequestFn
	onFileDelete  FileDeleteRequestFn
	onFileMkdir   FileMkdirRequestFn
	onFileMove    FileMoveRequestFn
	onSpoolSet    SetActiveSpoolFn
	onWebrtcOffer WebrtcOfferFn
	onWebrtcIce   WebrtcIceCandidateFn
	onWebrtcEnd   WebrtcSessionEndFn
	backoff       backoff.Config
}

func New(url, printerID, token, agentVer string, log *slog.Logger) *Client {
	return &Client{url: url, printerID: printerID, token: token, agentVer: agentVer, log: log, backoff: backoff.Default()}
}

// Run mantiene la conexión al hub viva con reconexión automática. El Handshake
// se re-envía en cada reconexión (lo hace Connect).
func (c *Client) Run(ctx context.Context) {
	runner.Run(ctx, c.log, "hub", c.backoff, func(ctx context.Context) (func(), func(context.Context) error, error) {
		if err := c.Connect(ctx); err != nil {
			return nil, nil, err
		}
		return func() { _ = c.Close() }, c.ReadLoop, nil
	})
}

// SetCommandCallback registra la función que se llama con cada Command del hub.
func (c *Client) SetCommandCallback(fn func(protocol.Command)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onCommand = fn
}

// SetFileListRequestCallback registra el handler para FileListRequest del hub.
func (c *Client) SetFileListRequestCallback(fn func(protocol.FileListRequest)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onFileList = fn
}

// SetFileGetRequestCallback registra el handler para FileGetRequest del hub.
func (c *Client) SetFileGetRequestCallback(fn func(protocol.FileGetRequest)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onFileGet = fn
}

// SetFileUploadRequestCallback registra el handler para FileUploadRequest del hub.
func (c *Client) SetFileUploadRequestCallback(fn func(protocol.FileUploadRequest)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onFileUpload = fn
}

// SetFileDeleteRequestCallback registra el handler para FileDeleteRequest del hub.
func (c *Client) SetFileDeleteRequestCallback(fn func(protocol.FileDeleteRequest)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onFileDelete = fn
}

// SetFileMkdirRequestCallback registra el handler para FileMkdirRequest del hub.
func (c *Client) SetFileMkdirRequestCallback(fn func(protocol.FileMkdirRequest)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onFileMkdir = fn
}

// SetFileMoveRequestCallback registra el handler para FileMoveRequest del hub.
func (c *Client) SetFileMoveRequestCallback(fn func(protocol.FileMoveRequest)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onFileMove = fn
}

// SetActiveSpoolCallback registra el handler para SetActiveSpool del hub.
func (c *Client) SetActiveSpoolCallback(fn func(protocol.SetActiveSpool)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onSpoolSet = fn
}

// SetWebrtcOfferCallback registra el handler para webrtc.offer del hub.
func (c *Client) SetWebrtcOfferCallback(fn func(protocol.WebrtcOffer)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onWebrtcOffer = fn
}

// SetWebrtcIceCandidateCallback registra el handler para webrtc.ice_candidate.
func (c *Client) SetWebrtcIceCandidateCallback(fn func(protocol.WebrtcIceCandidate)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onWebrtcIce = fn
}

// SetWebrtcSessionEndCallback registra el handler para webrtc.session_end.
func (c *Client) SetWebrtcSessionEndCallback(fn func(protocol.WebrtcSessionEnd)) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.onWebrtcEnd = fn
}

func (c *Client) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("tunnel: no se pudo conectar al hub %s: %w", c.url, err)
	}
	c.writeMu.Lock()
	c.conn = conn
	c.ready = false // aún no hemos completado el handshake
	c.writeMu.Unlock()
	c.log.Info("conectado al hub", "url", c.url)

	// Enviar Handshake real (escritura directa, aún no "ready").
	hs := protocol.Handshake{
		PrinterID:    c.printerID,
		Token:        c.token,
		AgentVersion: c.agentVer,
	}
	env, err := protocol.NewEnvelope(protocol.TypeHandshake, c.printerID, hs)
	if err != nil {
		return fmt.Errorf("tunnel: armar handshake: %w", err)
	}
	if err := c.write(env); err != nil {
		return fmt.Errorf("tunnel: enviar handshake: %w", err)
	}

	// A partir de acá la conexión está lista para mensajes regulares.
	c.writeMu.Lock()
	c.ready = true
	c.writeMu.Unlock()

	c.log.Info("handshake enviado al hub", "printer_id", c.printerID, "agent_version", c.agentVer)
	return nil
}

// authRejected devuelve true si el error de websocket es un cierre con el
// código que el hub usa para rechazar la autenticación.
func authRejected(err error) bool {
	return err != nil && websocket.CloseStatus(err) == websocket.StatusCode(protocol.CloseAuthRejected)
}

func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close(websocket.StatusNormalClosure, "shutdown")
	}
	return nil
}

// SendEnvelope envía un envelope al hub. Devuelve error si la conexión no está
// activa o si el handshake aún no se ha completado.
func (c *Client) SendEnvelope(ctx context.Context, env *protocol.Envelope) error {
	data, err := env.Encode()
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("tunnel: no hay conexión activa al hub")
	}
	if !c.ready {
		return fmt.Errorf("tunnel: handshake aún no completado")
	}
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// write escribe un envelope ya serializado sin chequear "ready" (usado por el
// handshake en Connect).
func (c *Client) write(env *protocol.Envelope) error {
	data, err := env.Encode()
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("tunnel: no hay conexión activa al hub")
	}
	return c.conn.Write(context.Background(), websocket.MessageText, data)
}

func (c *Client) ReadLoop(ctx context.Context) error {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			if authRejected(err) {
				return runner.ErrAuthRejected
			}
			return err
		}

		env, err := protocol.DecodeEnvelope(data)
		if err != nil {
			c.log.Warn("mensaje del hub no parseable", "error", err)
			continue
		}

		switch env.Type {
		case protocol.TypeCommand:
			var cmd protocol.Command
			if err := env.UnmarshalPayload(&cmd); err != nil {
				c.log.Warn("command del hub inválido", "error", err)
				continue
			}
			c.log.Info("command recibido del hub", "command_id", cmd.CommandID, "command_type", cmd.CommandType)
			if c.onCommand != nil {
				c.onCommand(cmd)
			}
		case protocol.TypeFileListRequest:
			var req protocol.FileListRequest
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("file_list_request del hub inválido", "error", err)
				continue
			}
			c.log.Info("file_list_request recibido del hub", "request_id", req.RequestID)
			if c.onFileList != nil {
				c.onFileList(req)
			}
		case protocol.TypeFileGetRequest:
			var req protocol.FileGetRequest
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("file_get_request del hub inválido", "error", err)
				continue
			}
			c.log.Info("file_get_request recibido del hub", "request_id", req.RequestID, "filename", req.Filename)
			if c.onFileGet != nil {
				c.onFileGet(req)
			}
		case protocol.TypeFileUploadRequest:
			var req protocol.FileUploadRequest
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("file_upload_request del hub inválido", "error", err)
				continue
			}
			c.log.Info("file_upload_request recibido del hub", "request_id", req.RequestID, "filename", req.Filename)
			if c.onFileUpload != nil {
				c.onFileUpload(req)
			}
		case protocol.TypeFileDeleteRequest:
			var req protocol.FileDeleteRequest
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("file_delete_request del hub inválido", "error", err)
				continue
			}
			c.log.Info("file_delete_request recibido del hub", "request_id", req.RequestID, "filename", req.Filename)
			if c.onFileDelete != nil {
				c.onFileDelete(req)
			}
		case protocol.TypeFileMkdirRequest:
			var req protocol.FileMkdirRequest
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("file_mkdir_request del hub inválido", "error", err)
				continue
			}
			c.log.Info("file_mkdir_request recibido del hub", "request_id", req.RequestID, "dirname", req.Dirname)
			if c.onFileMkdir != nil {
				c.onFileMkdir(req)
			}
		case protocol.TypeFileMoveRequest:
			var req protocol.FileMoveRequest
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("file_move_request del hub inválido", "error", err)
				continue
			}
			c.log.Info("file_move_request recibido del hub", "request_id", req.RequestID, "src", req.Src, "dst", req.Dst)
			if c.onFileMove != nil {
				c.onFileMove(req)
			}
		case protocol.TypeSetActiveSpool:
			var req protocol.SetActiveSpool
			if err := env.UnmarshalPayload(&req); err != nil {
				c.log.Warn("set_active_spool del hub inválido", "error", err)
				continue
			}
			c.log.Info("set_active_spool recibido del hub", "request_id", req.RequestID, "spool_id", req.SpoolID)
			if c.onSpoolSet != nil {
				c.onSpoolSet(req)
			}

		case protocol.TypeWebrtcOffer:
			var msg protocol.WebrtcOffer
			if err := env.UnmarshalPayload(&msg); err != nil {
				c.log.Warn("webrtc.offer del hub inválido", "error", err)
				continue
			}
			c.log.Info("webrtc.offer recibido del hub", "session_id", msg.SessionID)
			if c.onWebrtcOffer != nil {
				c.onWebrtcOffer(msg)
			}
		case protocol.TypeWebrtcIceCandidate:
			var msg protocol.WebrtcIceCandidate
			if err := env.UnmarshalPayload(&msg); err != nil {
				c.log.Warn("webrtc.ice_candidate del hub inválido", "error", err)
				continue
			}
			c.log.Debug("webrtc.ice_candidate recibido del hub", "session_id", msg.SessionID)
			if c.onWebrtcIce != nil {
				c.onWebrtcIce(msg)
			}
		case protocol.TypeWebrtcSessionEnd:
			var msg protocol.WebrtcSessionEnd
			if err := env.UnmarshalPayload(&msg); err != nil {
				c.log.Warn("webrtc.session_end del hub inválido", "error", err)
				continue
			}
			c.log.Info("webrtc.session_end recibido del hub", "session_id", msg.SessionID, "reason", msg.Reason)
			if c.onWebrtcEnd != nil {
				c.onWebrtcEnd(msg)
			}

		case protocol.TypeAck:
			var ack protocol.Ack
			if err := env.UnmarshalPayload(&ack); err != nil {
				continue
			}
			c.log.Debug("ack del hub", "command_id", ack.CommandID, "success", ack.Success)
		default:
			c.log.Warn("tipo de mensaje del hub desconocido", "type", env.Type)
		}
	}
}
