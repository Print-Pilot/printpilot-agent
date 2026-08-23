# printpilot-agent

Proceso liviano en Go que corre **en la máquina conectada a la impresora** (netbook, Raspberry Pi, etc). Es el puente entre Moonraker (local) y el hub (nube), sin exponer ningún puerto entrante.

```
[Tu impresora + Moonraker]
        │
        │  (conexión saliente WSS — sin puertos abiertos)
        ▼
[printpilot-agent]  →  corre junto a Moonraker, hace de puente
        │
        ▼
[printpilot-hub]    →  servidor central, gestiona conexiones y ruteo
```

## Estado

🚧 Fase 4 (producción) — autenticación real contra el panel (token en el Handshake, rechazo con cierre 4001 y espera larga si el token es inválido), build de release multi-arquitectura con GitHub Actions, e instalación como servicio systemd. Ver [PLAN.md](PLAN.md).

## Decisiones de librerías

| Librería | Decisión |
|---|---|
| WebSocket cliente | [`github.com/coder/websocket`](https://github.com/coder/websocket) (ex-nhooyr) |
| Config | YAML con [`gopkg.in/yaml.v3`](https://github.com/go-yaml/yaml) |
| Protocolo | [`github.com/Print-Pilot/printpilot-protocol`](https://github.com/Print-Pilot/printpilot-protocol) (vía `replace` local por ahora) |

Se agregan como dependencias en Fase 1, cuando haya código que las importe.

## Requisitos

- Go 1.25+
- Build cruzado para: `linux/amd64`, `linux/386`, `linux/arm` (GOARM=7), `linux/arm64`

## Build y uso

```sh
make build            # build nativo → dist/printpilot-agent
make build-386        # linux/386 (netbooks / hardware 32 bits)
make build-arm        # linux/arm
make build-arm64      # linux/arm64 (Raspberry Pi)
make release          # los 4 binarios con la versión (git describe)
make run ARGS="--config config.example.yaml"
make test
```

## Autenticación (Fase 4)

El agente envía su `token` en el `Handshake`. El hub lo valida contra la fuente
de verdad (el panel). Si el token es inválido, el hub cierra la conexión con el
código WebSocket `4001` y el agente lo interpreta: **no reintenta en loop
rápido**, sino que espera 60s y vuelve a intentar (para que al corregir la
config se recupere solo, sin saturar el hub).

## Instalación como servicio (Fase 4.3)

Desde GitHub Releases (binarios compilados por CI):

```sh
sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/download/v0.1.0/install.sh \
  | sudo bash -s -- v0.1.0
```

Instala el binario correcto según arquitectura en `/opt/printpilot-agent`, crea
el usuario `printpilot`, y registra el service `printpilot-agent` de systemd
con `Restart=always`. Completá `/etc/printpilot-agent/config.yaml` (hub_url,
printer_id, token) y luego `sudo systemctl restart printpilot-agent`.

Para probar localmente con el service del repo:

```sh
sudo install -m 644 deploy/printpilot-agent.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now printpilot-agent
```

## Config

## Probar (Fase 1)

Necesitás Moonraker corriendo en `ws://localhost:7125/websocket` (o ajustar `moonraker_url` en tu config).

```sh
# Terminal 1: servidor WSS de eco (solo para probar, no es parte del binario final)
make run-echo

# Terminal 2: agente, con el hub apuntando al echo server
make run ARGS="--config config.example.yaml"
```

Config de ejemplo para la prueba local:

```yaml
hub_url: ws://localhost:8787/ws
moonraker_url: ws://localhost:7125/websocket
printer_id: mi-printer
token: ""
```

Con el `hub_url` apuntando al echo server, el agente envía el saludo `hola, soy mi-printer` y ves el eco de vuelta por consola. Al mismo tiempo, los status updates de Moonraker se loguean en crudo.

## Config

Copiá `config.example.yaml` a `agent-config.yaml` y completá los valores. El archivo real está ignorado por git (ver `.gitignore`).

```yaml
hub_url: wss://hub.example.com/ws
token: ""
moonraker_url: ws://localhost:7125/websocket
printer_id: ""
heartbeat_interval_seconds: 30
log_file: "/tmp/printpilot-agent.log"   # opcional, "" = solo consola
log_level: "info"                        # debug | info | warn | error
spoolman_proxy_port: 8001               # proxy Spoolman local (0 = deshabilitado)
```

## Spoolman (reporte de filamento)

El agente expone un proxy local compatible con la API de Spoolman para que
Moonraker le reporte el consumo de filamento. El agente **no guarda inventario**:
solo recibe el reporte y lo reenvía al hub (que lo publica en Redis para que el
panel actualice el peso restante del spool).

Para activarlo:

1. En `moonraker.conf`, apuntá la integración de Spoolman a este proxy local:
   ```ini
   [spoolman]
   server: http://localhost:8001
   ```
   (ajustá el puerto si configuraste otro `spoolman_proxy_port`).

2. En `agent-config.yaml`:
   ```yaml
   spoolman_proxy_port: 8001
   ```

3. Reiniciá Moonraker y el agente. El agente loguea `proxy spoolman escuchando`
   y `Moonraker conectado al proxy spoolman (WS)` cuando Moonraker abre la
   conexión websocket.

Cada vez que Moonraker reporta uso (`PUT /api/v1/spool/{id}/use`), el agente
convierte el `use_length` (mm) en un `FilamentUsageReport` y lo envía al hub.
Si el tunnel está caído, los reportes se encolan en un buffer acotado (máx 1000)
y se reintentan cada 3s.

Contrato detallado de compatibilidad: [`SPOOLMAN_COMPAT.md`](SPOOLMAN_COMPAT.md).

## Reconexión

El agente se reconecta solo con backoff exponencial + jitter (initial 500ms, factor 2, max 30s) tanto a Moonraker como al hub, por separado. Tras cada reconexión al hub se re-envía el `Handshake` automáticamente.

## Estructura

```
printpilot-agent/
├── main.go
├── cmd/
│   ├── echoserver/      # servidor WSS de eco para pruebas (no parte del binario final)
│   └── moonmock/        # Moonraker simulado para pruebas (no parte del binario final)
├── internal/
│   ├── moonraker/      # cliente websocket de Moonraker
│   ├── tunnel/         # cliente WSS hacia el hub + reconexión
│   ├── bridge/         # lógica de reenvío entre ambos
│   ├── config/         # carga de config local
│   ├── backoff/        # backoff exponencial con jitter
│   └── runner/         # loop de conexión/reconexión genérico
├── config.example.yaml
└── Makefile
```