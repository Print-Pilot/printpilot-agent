package webrtc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Go2rtc abstrae el cliente a go2rtc (camino primario, WHEP). Se define como
// interfaz para poder testear la lógica del Manager con un fake.
//
// CONTRATO VALIDADO contra go2rtc v1.9.14 (2026-08-23):
//   - Endpoint real: POST /api/webrtc?src={camera} con Content-Type
//     application/sdp y el offer en el body. Responde 201 Created con el SDP
//     answer en el body (código H264/VP8/VP9 según lo que soporte el source).
//   - NO hay header Location/ETag ni id de sesión para el camino `?src=`
//     (consumidor): es fire-and-forget. La limpieza la hace go2rtc cuando el
//     peer se cierra (DTLS close del navegador → inmediato) o cuando el ICE
//     falla (self-clean con timeout de pion).
//   - Trickle ICE por HTTP NO está implementado: PATCH responde 405. Por eso
//     el offer debe incluir TODOS los candidates del navegador (non-trickle).
//   - No existe DELETE para sesiones WHEP (el endpoint DELETE /api/webrtc?id=
//     solo aplica a WHIP, `?dst=`). Por eso NO hay forma de cerrar la sesión
//     desde el agente: se cierra sola con el peer.
type Go2rtc interface {
	// CreatePeer crea un peer WHEP en go2rtc para la cámara y devuelve el SDP
	// answer. No devuelve handle de sesión (go2rtc no lo expone en este camino).
	CreatePeer(ctx context.Context, camera, offerSDP string) (answerSDP string, err error)
}

// Client es la implementación HTTP de Go2rtc (endpoint WHEP de go2rtc).
type Client struct {
	base string
	http *http.Client
}

// NewClient crea el cliente. base es la URL base de go2rtc (ej.
// http://localhost:1984).
func NewClient(base string) *Client {
	return &Client{
		base: strings.TrimSuffix(base, "/"),
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) CreatePeer(ctx context.Context, camera, offerSDP string) (string, error) {
	// pion/sdp (el parser que usa go2rtc en SetOffer) devuelve io.EOF si el
	// SDP no termina en newline. El offer del navegador puede llegar sin el
	// \n final: garantizarlo evita el HTTP 500 EOF de go2rtc.
	if !strings.HasSuffix(offerSDP, "\n") {
		offerSDP += "\n"
	}

	u := c.base + "/api/webrtc?src=" + url.QueryEscape(camera)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(offerSDP))
	if err != nil {
		return "", fmt.Errorf("webrtc: armar whep create: %w", err)
	}
	req.Header.Set("Content-Type", "application/sdp")
	req.Header.Set("Accept", "application/sdp")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("webrtc: whep create: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("webrtc: whep create (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("webrtc: leer answer: %w", err)
	}
	if len(answer) == 0 {
		return "", fmt.Errorf("webrtc: whep create devolvió answer vacío")
	}
	return string(answer), nil
}
