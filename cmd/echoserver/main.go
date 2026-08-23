package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/coder/websocket"
)

func main() {
	addr := flag.String("addr", "localhost:8787", "dirección del servidor de eco")
	flag.Parse()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "adiós")
		log.Printf("cliente conectado: %s", r.RemoteAddr)

		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				log.Printf("cliente desconectado: %v", err)
				return
			}
			log.Printf("recibido: %s", data)
			if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
				log.Printf("write: %v", err)
				return
			}
		}
	})

	log.Printf("servidor de eco listo en ws://%s/ws", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
