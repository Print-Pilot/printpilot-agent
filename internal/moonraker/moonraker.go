package moonraker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Print-Pilot/printpilot-agent/internal/backoff"
	"github.com/Print-Pilot/printpilot-agent/internal/runner"
)

// Status es el subset de estado de Moonraker que nos interesa.
type Status struct {
	State          string
	Progress       float64
	Filename       string
	PrintDuration  float64
	ExtruderTemp   float64
	ExtruderTarget float64
	BedTemp        float64
	BedTarget      float64
}

// StatusFn recibe cada status actualizado emitido por Moonraker.
type StatusFn func(Status)

type Client struct {
	url          string
	log          *slog.Logger
	conn         *websocket.Conn
	mu           sync.Mutex
	idCount      int
	status       Status
	onStatus     StatusFn
	backoff      backoff.Config
	http         *http.Client
	subscribed   []string
	pollInterval time.Duration
}

func New(url string, log *slog.Logger) *Client {
	return &Client{url: url, log: log, backoff: backoff.Default(), http: &http.Client{Timeout: 60 * time.Second}, pollInterval: 3 * time.Second}
}

// httpBase devuelve la URL base HTTP de Moonraker derivada de la URL del
// websocket (ws://host:7125/websocket -> http://host:7125).
func (c *Client) httpBase() string {
	base := c.url
	base = strings.TrimSuffix(base, "/websocket")
	base = strings.Replace(base, "ws://", "http://", 1)
	base = strings.Replace(base, "wss://", "https://", 1)
	return base
}

// Run mantiene la conexión viva con reconexión automática: conecta, se
// suscribe a objects, corre el read loop, y ante una caída espera con backoff
// y reintenta hasta que el contexto se cancele.
func (c *Client) Run(ctx context.Context, objects []string) {
	runner.Run(ctx, c.log, "moonraker", c.backoff, func(ctx context.Context) (func(), func(context.Context) error, error) {
		if err := c.Connect(ctx); err != nil {
			return nil, nil, err
		}
		if err := c.Subscribe(ctx, objects); err != nil {
			_ = c.Close()
			return nil, nil, err
		}
		return func() { _ = c.Close() }, c.ReadLoop, nil
	})
}

func (c *Client) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("moonraker: no se pudo conectar a %s: %w", c.url, err)
	}
	c.conn = conn
	c.log.Info("conectado a Moonraker", "url", c.url)
	return nil
}

func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close(websocket.StatusNormalClosure, "shutdown")
	}
	return nil
}

// SetStatusCallback registra la función que se llama con cada actualización de estado.
func (c *Client) SetStatusCallback(fn StatusFn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStatus = fn
}

func (c *Client) Subscribe(ctx context.Context, objects []string) error {
	params := make(map[string]any, len(objects))
	for _, o := range objects {
		params[o] = nil
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "printer.objects.subscribe",
		"params":  map[string]any{"objects": params},
		"id":      c.nextID(),
	}
	if err := c.writeJSON(ctx, req); err != nil {
		return fmt.Errorf("moonraker: enviar suscripción: %w", err)
	}

	c.mu.Lock()
	c.subscribed = append([]string(nil), objects...)
	c.mu.Unlock()

	c.log.Info("suscrito a objetos de Moonraker", "objects", objects)
	return nil
}

// subscribedObjects devuelve una copia de los objetos suscritos.
func (c *Client) subscribedObjects() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.subscribed...)
}

// QueryStatus consulta el estado actual de los objetos suscritos vía
// printer.objects.query. La respuesta se procesa en handleMessage como si
// fuera un notify_status_update. Se usa para refrescar temperaturas/estado
// cuando Moonraker no emite cambios (p. ej. estando en standby).
func (c *Client) QueryStatus(ctx context.Context, objects []string) error {
	params := make(map[string]any, len(objects))
	for _, o := range objects {
		params[o] = nil
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "printer.objects.query",
		"params":  map[string]any{"objects": params},
		"id":      c.nextID(),
	}
	return c.writeJSON(ctx, req)
}

// ReadLoop lee mensajes de Moonraker y actualiza el estado, llamando al callback.
// Además hace polling periódico de printer.objects.query para refrescar el
// estado aunque Moonraker no emita cambios por push.
func (c *Client) ReadLoop(ctx context.Context) error {
	objects := c.subscribedObjects()

	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll()

	go func() {
		ticker := time.NewTicker(c.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				if err := c.QueryStatus(pollCtx, objects); err != nil {
					c.log.Warn("error consultando estado a Moonraker", "error", err)
				}
			}
		}
	}()

	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return err
		}
		c.handleMessage(ctx, data)
	}
}

// pause/resume/cancel/gcode se ejecutan vía la API JSON-RPC de Moonraker.

func (c *Client) Pause(ctx context.Context) error {
	return c.callAction(ctx, "printer.pause_resume.pause")
}

func (c *Client) Resume(ctx context.Context) error {
	return c.callAction(ctx, "printer.pause_resume.resume")
}

func (c *Client) Cancel(ctx context.Context) error {
	return c.callAction(ctx, "printer.pause_resume.cancel")
}

func (c *Client) SendGcode(ctx context.Context, gcode string) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "printer.gcode.script",
		"params":  map[string]any{"script": gcode},
		"id":      c.nextID(),
	}
	if err := c.writeJSON(ctx, req); err != nil {
		return fmt.Errorf("moonraker: enviar gcode: %w", err)
	}
	return nil
}

func (c *Client) callAction(ctx context.Context, method string) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      c.nextID(),
	}
	if err := c.writeJSON(ctx, req); err != nil {
		return fmt.Errorf("moonraker: llamar %s: %w", method, err)
	}
	return nil
}

// handleMessage procesa un mensaje crudo de Moonraker.
func (c *Client) handleMessage(ctx context.Context, data []byte) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		c.log.Warn("mensaje de Moonraker no parseable", "error", err)
		return
	}

	switch {
	case msg.Method == "notify_status_update":
		var params []json.RawMessage
		if err := json.Unmarshal(msg.Params, &params); err != nil || len(params) < 1 {
			return
		}
		var partial map[string]json.RawMessage
		if err := json.Unmarshal(params[0], &partial); err != nil {
			return
		}
		c.applyPartial(partial)
	case len(msg.Result) > 0:
		// Respuesta a subscribe: {"status": {...}}
		var res struct {
			Status map[string]json.RawMessage `json:"status"`
		}
		if err := json.Unmarshal(msg.Result, &res); err == nil && len(res.Status) > 0 {
			c.applyPartial(res.Status)
		}
	}
}

// applyPartial fusiona un map de objetos de estado en el Status acumulado.
func (c *Client) applyPartial(partial map[string]json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// print_stats
	if psRaw, ok := partial["print_stats"]; ok {
		// OJO: Moonraker manda print_stats PARCIAL (solo los campos que
		// cambiaron). state no siempre viene: pisarlo con "" acá borraba el
		// estado acumulado y el bridge re-disparaba print_started en cada poll.
		var ps struct {
			State         *string `json:"state"`
			Filename      *string `json:"filename"`
			PrintDuration float64 `json:"print_duration"`
		}
		if err := json.Unmarshal(psRaw, &ps); err == nil {
			if ps.State != nil {
				c.status.State = *ps.State
			}
			if ps.Filename != nil {
				c.status.Filename = *ps.Filename
			}
			c.status.PrintDuration = ps.PrintDuration
		}
	}
	// extruder
	if exRaw, ok := partial["extruder"]; ok {
		var ex struct {
			Temperature float64 `json:"temperature"`
			Target      float64 `json:"target"`
		}
		if err := json.Unmarshal(exRaw, &ex); err == nil {
			c.status.ExtruderTemp = ex.Temperature
			c.status.ExtruderTarget = ex.Target
		}
	}
	// heater_bed
	if bedRaw, ok := partial["heater_bed"]; ok {
		var bed struct {
			Temperature float64 `json:"temperature"`
			Target      float64 `json:"target"`
		}
		if err := json.Unmarshal(bedRaw, &bed); err == nil {
			c.status.BedTemp = bed.Temperature
			c.status.BedTarget = bed.Target
		}
	}

	if c.onStatus != nil {
		c.onStatus(c.status)
	}
}

func (c *Client) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idCount++
	return c.idCount
}

func (c *Client) writeJSON(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("moonraker: no hay conexión activa")
	}
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// FileInfo describe un archivo .gcode devuelto por Moonraker.
type FileInfo struct {
	Filename string
	Size     int64
	Modified int64
}

// ListFiles devuelve la lista de archivos .gcode de Moonraker.
func (c *Client) ListFiles(ctx context.Context) ([]FileInfo, error) {
	u := c.httpBase() + "/server/files/list?root=gcodes"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("moonraker: armar listado: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moonraker: listar archivos: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("moonraker: listar archivos (HTTP %d)", resp.StatusCode)
	}

	var out struct {
		Result []struct {
			Path     string  `json:"path"`
			Size     int64   `json:"size"`
			Modified float64 `json:"modified"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("moonraker: parsear listado: %w", err)
	}

	files := make([]FileInfo, 0, len(out.Result))
	for _, f := range out.Result {
		files = append(files, FileInfo{Filename: f.Path, Size: f.Size, Modified: int64(f.Modified)})
	}
	return files, nil
}

// ReadFileContent lee el archivo gcode completo de Moonraker y devuelve su
// contenido y tamaño. Los archivos suelen ser de 1-2MB; el bridge lo manda
// fragmentado al hub si hace falta.
func (c *Client) ReadFileContent(ctx context.Context, filename string) ([]byte, int64, error) {
	u := c.httpBase() + "/server/files/gcodes/" + url.PathEscape(filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("moonraker: armar lectura: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("moonraker: leer archivo %s: %w", filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("moonraker: leer archivo %s (HTTP %d)", filename, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("moonraker: leer cuerpo %s: %w", filename, err)
	}
	return body, int64(len(body)), nil
}

// UploadFile guarda (o reemplaza) un archivo .gcode en Moonraker. Escribe el
// contenido en un archivo temporal y lo sube vía la API de subida de Moonraker.
func (c *Client) UploadFile(ctx context.Context, filename string, data []byte) error {
	u := c.httpBase() + "/server/files/upload?root=gcodes"

	body := &bytes.Buffer{}
	multipart := multipart.NewWriter(body)
	part, err := multipart.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("moonraker: crear multipart: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("moonraker: escribir multipart: %w", err)
	}
	if err := multipart.Close(); err != nil {
		return fmt.Errorf("moonraker: cerrar multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return fmt.Errorf("moonraker: armar subida: %w", err)
	}
	req.Header.Set("Content-Type", multipart.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("moonraker: subir archivo %s: %w", filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("moonraker: subir archivo %s (HTTP %d): %s", filename, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// DeleteFile elimina un archivo .gcode de Moonraker.
func (c *Client) DeleteFile(ctx context.Context, filename string) error {
	u := c.httpBase() + "/server/files/gcodes/" + url.PathEscape(filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("moonraker: armar delete: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("moonraker: eliminar archivo %s: %w", filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("moonraker: eliminar %s (HTTP %d): %s", filename, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// Mkdir crea un directorio dentro de gcodes en Moonraker.
// La API espera: POST /server/files/directory con body {"path": "gcodes/<dirname>"}
func (c *Client) Mkdir(ctx context.Context, dirname string) error {
	u := c.httpBase() + "/server/files/directory"
	payload, _ := json.Marshal(map[string]string{"path": "gcodes/" + dirname})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("moonraker: armar mkdir: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("moonraker: mkdir %s: %w", dirname, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("moonraker: mkdir %s (HTTP %d): %s", dirname, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// MoveFile renombra o mueve un archivo dentro de gcodes en Moonraker.
// La API espera: POST /server/files/move con JSON {"source": "gcodes/<src>", "dest": "gcodes/<dst>"}
func (c *Client) MoveFile(ctx context.Context, src, dst string) error {
	u := c.httpBase() + "/server/files/move"
	payload, _ := json.Marshal(map[string]string{"source": "gcodes/" + src, "dest": "gcodes/" + dst})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("moonraker: armar move: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("moonraker: mover %s -> %s: %w", src, dst, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("moonraker: mover %s -> %s (HTTP %d): %s", src, dst, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
