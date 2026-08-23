package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/coder/websocket"
)

// moonmock es un Moonraker simulado para pruebas locales:
// - acepta subscribe y responde con un estado inicial
// - emite notify_status_update periódicamente
// - responde ok a printer.pause_resume.pause / resume / cancel y gcode.script

func main() {
	addr := flag.String("addr", ":7125", "dirección del moonraker simulado")
	flag.Parse()

	http.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "adiós")
		log.Printf("cliente conectado: %s", r.RemoteAddr)

		ctx := context.Background()
		status := map[string]any{
			"print_stats": map[string]any{"state": "standby", "filename": "", "print_duration": 0.0},
			"extruder":    map[string]any{"temperature": 23.2, "target": 0.0},
			"heater_bed":  map[string]any{"temperature": 23.3, "target": 0.0},
		}

		go func() {
			// emitir notify_status_update cada segundo
			i := 0
			for {
				i++
				update := map[string]any{
					"extruder":    map[string]any{"temperature": 23.2 + float64(i%10)*0.1},
					"print_stats": map[string]any{"state": "printing", "progress": float64(i % 100)},
				}
				msg := map[string]any{
					"jsonrpc": "2.0",
					"method":  "notify_status_update",
					"params":  []any{update, float64(1000 + i)},
				}
				data, _ := json.Marshal(msg)
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					return
				}
			}
		}()

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				log.Printf("cliente desconectado: %v", err)
				return
			}
			var req struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				ID     int             `json:"id"`
			}
			_ = json.Unmarshal(data, &req)
			log.Printf("request: %s", req.Method)

			switch req.Method {
			case "printer.objects.subscribe":
				resp := map[string]any{
					"jsonrpc": "2.0",
					"result":  map[string]any{"status": status, "eventtime": 1000.0},
					"id":      req.ID,
				}
				data, _ := json.Marshal(resp)
				_ = conn.Write(ctx, websocket.MessageText, data)
			case "printer.pause_resume.pause", "printer.pause_resume.resume",
				"printer.pause_resume.cancel", "printer.gcode.script":
				resp := map[string]any{
					"jsonrpc": "2.0",
					"result":  map[string]any{},
					"id":      req.ID,
				}
				data, _ := json.Marshal(resp)
				_ = conn.Write(ctx, websocket.MessageText, data)
			default:
				resp := map[string]any{
					"jsonrpc": "2.0",
					"result":  map[string]any{},
					"id":      req.ID,
				}
				data, _ := json.Marshal(resp)
				_ = conn.Write(ctx, websocket.MessageText, data)
			}
		}
	})

	log.Printf("moonraker simulado listo en ws://localhost%s/websocket", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
