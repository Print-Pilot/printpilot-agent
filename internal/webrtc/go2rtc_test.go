package webrtc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWhepCreatePeer verifica el flujo WHEP validado: POST /api/webrtc?src=,
// 201 Created, answer en el body (sin header Location — go2rtc no lo expone).
func TestWhepCreatePeer(t *testing.T) {
	var gotQuery, gotOffer, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.URL.Path != "/api/webrtc" {
			t.Fatalf("ruta inesperada: %s", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("src")
		body, _ := io.ReadAll(r.Body)
		gotOffer = string(body)
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0\r\nanswer"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	answer, err := c.CreatePeer(context.Background(), "camara", "v=0\r\noffer")
	if err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	if gotMethod != http.MethodPost || gotQuery != "camara" || gotOffer != "v=0\r\noffer\n" {
		t.Fatalf("method=%q query=%q offer=%q", gotMethod, gotQuery, gotOffer)
	}
	if answer != "v=0\r\nanswer" {
		t.Fatalf("answer inesperado: %q", answer)
	}
}

// TestCreatePeerAppendsTrailingNewline: pion/sdp devuelve io.EOF si el SDP no
// termina en newline; el cliente debe garantizarlo antes de enviarlo.
func TestCreatePeerAppendsTrailingNewline(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("v=0\nanswer"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	offer := "v=0\no=- 0 0 IN IP4 0.0.0.0\ns=-\nt=0 0" // sin \n final
	if _, err := c.CreatePeer(context.Background(), "default", offer); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	if !strings.HasSuffix(gotBody, "\n") {
		t.Errorf("el body enviado a go2rtc no termina en newline: %q", gotBody)
	}
	if strings.Contains(gotBody, "\n\n") {
		t.Errorf("newline duplicado en el body: %q", gotBody)
	}
}

// TestWhepCreatePeer404: un source inexistente → 404 con mensaje.
func TestWhepCreatePeer404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stream not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.CreatePeer(context.Background(), "nope", "v=0\r\nx")
	if err == nil {
		t.Fatalf("se esperaba error por 404")
	}
}

// TestWhepCreatePeer500: un answer con error de ffmpeg → 500 con el mensaje.
func TestWhepCreatePeer500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ffmpeg: no such input", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.CreatePeer(context.Background(), "test", "v=0\r\nx")
	if err == nil {
		t.Fatalf("se esperaba error por 500")
	}
}
