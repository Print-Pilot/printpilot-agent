package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Print-Pilot/printpilot-agent/internal/moonraker"
	"github.com/Print-Pilot/printpilot-protocol"
)

// Tunnel es lo que el bridge necesita del cliente al hub. La define el bridge
// para poder testear la lógica con un mock.
type Tunnel interface {
	SendEnvelope(ctx context.Context, env *protocol.Envelope) error
	SetCommandCallback(fn func(protocol.Command))
	SetFileListRequestCallback(fn func(protocol.FileListRequest))
	SetFileGetRequestCallback(fn func(protocol.FileGetRequest))
	SetFileUploadRequestCallback(fn func(protocol.FileUploadRequest))
	SetFileDeleteRequestCallback(fn func(protocol.FileDeleteRequest))
	SetFileMkdirRequestCallback(fn func(protocol.FileMkdirRequest))
	SetFileMoveRequestCallback(fn func(protocol.FileMoveRequest))
}

type Bridge struct {
	log       *slog.Logger
	printerID string
	moon      *moonraker.Client
	tun       Tunnel
	startTime time.Time
	prevState string // estado anterior de print_stats para detectar transiciones
}

func New(printerID string, moon *moonraker.Client, tun Tunnel, log *slog.Logger) *Bridge {
	return &Bridge{
		log:       log,
		printerID: printerID,
		moon:      moon,
		tun:       tun,
		startTime: time.Now(),
	}
}

// Start conecta los callbacks: moonraker → status, hub → command, hub → file requests.
func (b *Bridge) Start() {
	b.moon.SetStatusCallback(b.onMoonrakerStatus)
	b.tun.SetCommandCallback(b.onHubCommand)
	b.tun.SetFileListRequestCallback(b.onFileListRequest)
	b.tun.SetFileGetRequestCallback(b.onFileGetRequest)
	b.tun.SetFileUploadRequestCallback(b.onFileUploadRequest)
	b.tun.SetFileDeleteRequestCallback(b.onFileDeleteRequest)
	b.tun.SetFileMkdirRequestCallback(b.onFileMkdirRequest)
	b.tun.SetFileMoveRequestCallback(b.onFileMoveRequest)
}

// onMoonrakerStatus transforma el status de Moonraker a StatusUpdate y lo envía al hub.
func (b *Bridge) onMoonrakerStatus(st moonraker.Status) {
	su := protocol.StatusUpdate{
		State:          st.State,
		Progress:       st.Progress,
		Filename:       st.Filename,
		PrintDuration:  st.PrintDuration,
		ExtruderTemp:   st.ExtruderTemp,
		ExtruderTarget: st.ExtruderTarget,
		BedTemp:        st.BedTemp,
		BedTarget:      st.BedTarget,
	}
	env, err := protocol.NewEnvelope(protocol.TypeStatusUpdate, b.printerID, su)
	if err != nil {
		b.log.Error("armar status update", "error", err)
		return
	}
	if err := b.tun.SendEnvelope(context.Background(), env); err != nil {
		b.log.Warn("enviar status update", "error", err)
	}

	b.detectEventTransition(st)
}

// detectEventTransition compara el estado actual con el anterior y emite un
// protocol.Event cuando Klipper pasa de un estado a otro (inicio, fin, fallo).
func (b *Bridge) detectEventTransition(st moonraker.Status) {
	curr := st.State
	prev := b.prevState
	b.prevState = curr

	// Estados "activos" (imprimiendo o en pausa durante una impresión).
	active := func(s string) bool { return s == "printing" || s == "paused" }

	switch {
	case prev != "printing" && curr == "printing":
		b.emitEvent(protocol.EventPrintStarted, "impresión iniciada", map[string]string{
			"filename": st.Filename,
		})
	case active(prev) && curr == "complete":
		b.emitEvent(protocol.EventPrintFinished, "impresión finalizada", map[string]string{
			"filename":       st.Filename,
			"print_duration": fmt.Sprintf("%.1f", st.PrintDuration),
		})
	case active(prev) && curr == "error":
		b.emitEvent(protocol.EventPrintFailed, "error en la impresión", map[string]string{
			"filename": st.Filename,
		})
	case active(prev) && curr == "cancelled":
		b.emitEvent(protocol.EventPrintFailed, "impresión cancelada", map[string]string{
			"filename": st.Filename,
		})
	}
}

// emitEvent envía un protocol.Event al hub con el tipo, mensaje y metadata dados.
func (b *Bridge) emitEvent(eventType, message string, metadata map[string]string) {
	ev := protocol.Event{
		EventType: eventType,
		Message:   message,
		Metadata:  metadata,
	}
	env, err := protocol.NewEnvelope(protocol.TypeEvent, b.printerID, ev)
	if err != nil {
		b.log.Error("armar evento", "event_type", eventType, "error", err)
		return
	}
	if err := b.tun.SendEnvelope(context.Background(), env); err != nil {
		b.log.Warn("enviar evento", "event_type", eventType, "error", err)
		return
	}
	b.log.Info("evento enviado al hub", "event_type", eventType, "printer_id", b.printerID, "metadata", metadata)
}

// onHubCommand ejecuta el comando contra Moonraker y responde con un Ack.
func (b *Bridge) onHubCommand(cmd protocol.Command) {
	ctx := context.Background()

	var err error
	switch cmd.CommandType {
	case protocol.CommandPause:
		err = b.moon.Pause(ctx)
	case protocol.CommandResume:
		err = b.moon.Resume(ctx)
	case protocol.CommandCancel:
		err = b.moon.Cancel(ctx)
	case protocol.CommandGcode:
		err = b.moon.SendGcode(ctx, cmd.Payload)
	default:
		err = fmt.Errorf("tipo de comando desconocido: %s", cmd.CommandType)
	}

	ack := protocol.Ack{CommandID: cmd.CommandID, Success: err == nil}
	if err != nil {
		ack.ErrorMessage = err.Error()
		b.log.Warn("comando falló", "command_id", cmd.CommandID, "command_type", cmd.CommandType, "error", err)
	} else {
		b.log.Info("comando ejecutado", "command_id", cmd.CommandID, "command_type", cmd.CommandType)
	}

	env, envErr := protocol.NewEnvelope(protocol.TypeAck, b.printerID, ack)
	if envErr != nil {
		b.log.Error("armar ack", "error", envErr)
		return
	}
	if sendErr := b.tun.SendEnvelope(ctx, env); sendErr != nil {
		b.log.Warn("enviar ack", "error", sendErr)
	}
}

// onFileListRequest lista los archivos .gcode de Moonraker y responde.
func (b *Bridge) onFileListRequest(req protocol.FileListRequest) {
	ctx := context.Background()
	files, err := b.moon.ListFiles(ctx)
	resp := protocol.FileList{RequestID: req.RequestID, Success: err == nil}
	if err != nil {
		resp.Error = err.Error()
		b.log.Warn("listar archivos falló", "request_id", req.RequestID, "error", err)
	} else {
		resp.Files = make([]protocol.FileInfo, len(files))
		for i, f := range files {
			resp.Files[i] = protocol.FileInfo{Filename: f.Filename, Size: f.Size, Modified: f.Modified}
		}
	}
	env, envErr := protocol.NewEnvelope(protocol.TypeFileList, b.printerID, resp)
	if envErr != nil {
		b.log.Error("armar file_list", "error", envErr)
		return
	}
	if sendErr := b.tun.SendEnvelope(ctx, env); sendErr != nil {
		b.log.Warn("enviar file_list", "error", sendErr)
	}
}

// onFileGetRequest lee el contenido de un archivo de Moonraker y lo envía al
// hub por fragmentos (el websocket tiene un límite de 32KB por mensaje).
func (b *Bridge) onFileGetRequest(req protocol.FileGetRequest) {
	const chunkSize = 24 * 1024 // 24KB por fragmento (deja margen para el envelope JSON)
	ctx := context.Background()

	data, total, err := b.moon.ReadFileContent(ctx, req.Filename)
	if err != nil {
		b.log.Warn("leer archivo falló", "request_id", req.RequestID, "filename", req.Filename, "error", err)
		b.sendFileContentError(req, err, total)
		return
	}

	for offset := int64(0); offset < int64(len(data)); offset += chunkSize {
		end := offset + chunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}

		isEOF := end >= int64(len(data))
		chunk := protocol.FileContent{
			RequestID: req.RequestID,
			Filename:  req.Filename,
			Offset:    offset,
			Data:      string(data[offset:end]),
			EOF:       isEOF,
			TotalSize: total,
			Success:   true,
		}

		env, envErr := protocol.NewEnvelope(protocol.TypeFileContent, b.printerID, chunk)
		if envErr != nil {
			b.log.Error("armar file_content chunk", "error", envErr)
			return
		}
		if sendErr := b.tun.SendEnvelope(ctx, env); sendErr != nil {
			b.log.Warn("enviar file_content chunk", "offset", offset, "error", sendErr)
			return
		}
	}

	b.log.Info("archivo enviado por chunks", "request_id", req.RequestID, "filename", req.Filename, "total_size", total)
}

func (b *Bridge) sendFileContentError(req protocol.FileGetRequest, err error, total int64) {
	resp := protocol.FileContent{
		RequestID: req.RequestID,
		Filename:  req.Filename,
		Success:   false,
		Error:     err.Error(),
		TotalSize: total,
		EOF:       true,
	}
	env, envErr := protocol.NewEnvelope(protocol.TypeFileContent, b.printerID, resp)
	if envErr != nil {
		return
	}
	_ = b.tun.SendEnvelope(context.Background(), env)
}

// onFileUploadRequest recibe un archivo del hub y lo sube a Moonraker.
func (b *Bridge) onFileUploadRequest(req protocol.FileUploadRequest) {
	ctx := context.Background()
	err := b.moon.UploadFile(ctx, req.Filename, []byte(req.Data))
	ack := protocol.FileUploadAck{RequestID: req.RequestID, Success: err == nil}
	if err != nil {
		ack.Error = err.Error()
		b.log.Warn("subir archivo falló", "request_id", req.RequestID, "filename", req.Filename, "error", err)
	} else {
		b.log.Info("archivo subido a Moonraker", "request_id", req.RequestID, "filename", req.Filename)
	}
	env, envErr := protocol.NewEnvelope(protocol.TypeFileUploadAck, b.printerID, ack)
	if envErr != nil {
		b.log.Error("armar file_upload_ack", "error", envErr)
		return
	}
	if sendErr := b.tun.SendEnvelope(ctx, env); sendErr != nil {
		b.log.Warn("enviar file_upload_ack", "error", sendErr)
	}
}

// onFileDeleteRequest elimina un archivo de Moonraker.
func (b *Bridge) onFileDeleteRequest(req protocol.FileDeleteRequest) {
	ctx := context.Background()
	err := b.moon.DeleteFile(ctx, req.Filename)
	ack := protocol.FileDeleteAck{RequestID: req.RequestID, Success: err == nil}
	if err != nil {
		ack.Error = err.Error()
		b.log.Warn("eliminar archivo falló", "request_id", req.RequestID, "filename", req.Filename, "error", err)
	} else {
		b.log.Info("archivo eliminado de Moonraker", "request_id", req.RequestID, "filename", req.Filename)
	}
	env, envErr := protocol.NewEnvelope(protocol.TypeFileDeleteAck, b.printerID, ack)
	if envErr != nil {
		return
	}
	_ = b.tun.SendEnvelope(ctx, env)
}

// onFileMkdirRequest crea un directorio en Moonraker.
func (b *Bridge) onFileMkdirRequest(req protocol.FileMkdirRequest) {
	ctx := context.Background()
	err := b.moon.Mkdir(ctx, req.Dirname)
	ack := protocol.FileMkdirAck{RequestID: req.RequestID, Success: err == nil}
	if err != nil {
		ack.Error = err.Error()
		b.log.Warn("mkdir falló", "request_id", req.RequestID, "dirname", req.Dirname, "error", err)
	} else {
		b.log.Info("directorio creado en Moonraker", "request_id", req.RequestID, "dirname", req.Dirname)
	}
	env, envErr := protocol.NewEnvelope(protocol.TypeFileMkdirAck, b.printerID, ack)
	if envErr != nil {
		return
	}
	_ = b.tun.SendEnvelope(ctx, env)
}

// onFileMoveRequest mueve/renombrar un archivo en Moonraker.
func (b *Bridge) onFileMoveRequest(req protocol.FileMoveRequest) {
	ctx := context.Background()
	err := b.moon.MoveFile(ctx, req.Src, req.Dst)
	ack := protocol.FileMoveAck{RequestID: req.RequestID, Success: err == nil}
	if err != nil {
		ack.Error = err.Error()
		b.log.Warn("mover archivo falló", "request_id", req.RequestID, "src", req.Src, "dst", req.Dst, "error", err)
	} else {
		b.log.Info("archivo movido en Moonraker", "request_id", req.RequestID, "src", req.Src, "dst", req.Dst)
	}
	env, envErr := protocol.NewEnvelope(protocol.TypeFileMoveAck, b.printerID, ack)
	if envErr != nil {
		return
	}
	_ = b.tun.SendEnvelope(ctx, env)
}

// HeartbeatLoop envía un heartbeat periódico al hub.
func (b *Bridge) HeartbeatLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			hb := protocol.Heartbeat{
				UptimeSeconds: int64(time.Since(b.startTime).Seconds()),
			}
			env, err := protocol.NewEnvelope(protocol.TypeHeartbeat, b.printerID, hb)
			if err != nil {
				continue
			}
			if err := b.tun.SendEnvelope(ctx, env); err != nil {
				b.log.Warn("enviar heartbeat", "error", err)
			}
		}
	}
}
