# printpilot-agent

**El puente entre tu impresora 3D (Klipper + Moonraker) y PrintPilot** — liviano, seguro y remoto, sin exponer ningún puerto.

El agente corre **en la misma máquina que tu impresora** (Raspberry Pi, netbook, mini-PC…) y abre una única **conexión saliente** hacia el hub de PrintPilot. Desde ahí el ecosistema puede monitorear el estado de la impresora, controlar las impresiones y hasta ver la cámara en vivo — sin abrir puertos entrantes en tu red.

```
[Tu impresora + Klipper + Moonraker]
        │
        │  conexión saliente WSS (sin puertos abiertos)
        ▼
[printpilot-agent]  →  corre junto a Moonraker
        │
        ▼
[printpilot-hub]    →  servidor central
        │
        ▼
[printpilot-panel]  →  dashboard web
```

---

## Características

- **Telemetría en vivo**: estado, progreso y temperaturas de la impresión → panel.
- **Control remoto**: pausar, reanudar, cancelar y enviar g-code desde el panel.
- **Gestión de archivos**: listar, ver, subir, borrar y organizar los `.gcode`.
- **Cámara en vivo (WebRTC P2P)**: el video fluye **directo entre tu navegador y la impresora** (vía go2rtc), sin pasar por el hub ni el servidor.
- **Filamento (Spoolman)**: reporta el consumo real de filamento para que el panel descuente el peso de tus spools.
- **Auto-reconexión**: se recupera solo de caídas de red o reinicios de Moonraker.
- **Instalador interactivo**: menú con flechas y colores (`install` / `update` / `delete` / `check`).

---

## Requisitos

Para que el agente sea útil necesitás, **en la máquina de la impresora**:

| Dependencia | Necesaria para |
|---|---|
| **Klipper** | El firmware de la impresora |
| **Moonraker** | La API/websocket que el agente usa para hablar con Klipper (**obligatorio**) |
| Linux + **systemd** | Correr el agente como servicio |
| Crowsnest *(opcional)* | Captura de cámara |
| go2rtc *(opcional)* | Streaming de cámara en vivo |
| Go *(opcional)* | Solo si querés compilar desde fuente |

> El agente es una sola **binario estático**: no necesita Go instalado en producción.

---

## Instalación

### Rápido (desde GitHub Releases)

```sh
sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
  | sudo bash -s -- install
```

El instalador:

1. Chequea las dependencias del sistema (Klipper, Moonraker, Crowsnest, go2rtc…) y las muestra en un reporte con colores.
2. Descarga el binario correcto para tu arquitectura (amd64 / 386 / arm / arm64) y verifica su checksum.
3. Crea el usuario `printpilot` y genera la config.
4. Registra el servicio de systemd `printpilot-agent` y lo arranca.

### Interactivo (menú con flechas)

Corré el instalador **sin argumentos en una terminal** y vas a ver un menú navegable con **↑/↓ + Enter**:

```sh
sudo ./install.sh
```

### Configurar sin prompts

Podés evitar las preguntas pasando variables de entorno:

```sh
sudo PRINTPILOT_HUB_URL=wss://hub.example.com/ws \
     PRINTPILOT_PRINTER_ID=mi-impresora \
     PRINTPILOT_TOKEN=tu-token \
     PRINTPILOT_MOONRAKER_URL=ws://localhost:7125/websocket \
     bash -c "$(curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh)" -- install
```

Variables: `PRINTPILOT_HUB_URL`, `PRINTPILOT_PRINTER_ID`, `PRINTPILOT_TOKEN`, `PRINTPILOT_MOONRAKER_URL`, `PRINTPILOT_GO2RTC_URL`, `PRINTPILOT_BASE_URL` (mirror/proxy de descarga).

### A mano (binario + systemd)

```sh
# binario según arquitectura
curl -fsSL -o /usr/local/bin/printpilot-agent \
  https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/printpilot-agent-linux-amd64
chmod +x /usr/local/bin/printpilot-agent

# config
sudo mkdir -p /etc/printpilot-agent
sudo cp config.example.yaml /etc/printpilot-agent/config.yaml
# ... editá hub_url / printer_id / token ...

# servicio
sudo install -m 644 deploy/printpilot-agent.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now printpilot-agent
```

---

## Actualizar y desinstalar

```sh
# actualizar a la última versión y reiniciar el servicio
sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
  | sudo bash -s -- update

# desinstalar (la config se respalda en /etc/printpilot-agent/config.yaml.bak.*)
sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
  | sudo bash -s -- delete

# solo revisar dependencias y estado
sudo ./install.sh check
```

---

## Configuración

El archivo de config vive en `/etc/printpilot-agent/config.yaml` (o donde indiques con `--config`).

| Campo | Descripción |
|---|---|
| `hub_url` | URL del hub de PrintPilot (`wss://…`). **Obligatorio.** |
| `printer_id` | Identificador único de esta impresora en el ecosistema. **Obligatorio.** |
| `token` | Token de autenticación del agente (lo genera/valida el panel). Vacío = solo desarrollo. |
| `moonraker_url` | WebSocket de Moonraker (default `ws://localhost:7125/websocket`). |
| `heartbeat_interval_seconds` | Frecuencia del heartbeat al hub (default 30). |
| `log_file` | Archivo de log con rotación; vacío = solo consola. |
| `log_level` | `debug` \| `info` \| `warn` \| `error` (default `info`). |
| `spoolman_proxy_port` | Puerto del proxy Spoolman local (default 8001). |
| `go2rtc_url` | URL de go2rtc para la cámara (vacío = deshabilitado). |
| `camera_stream` | Stream de go2rtc a servir para la cámara "default". |
| `webrtc_timeout_seconds` | Timeout de negociación WebRTC (default 15). |

```yaml
hub_url: wss://hub.example.com/ws
token: ""
moonraker_url: ws://localhost:7125/websocket
printer_id: ""
heartbeat_interval_seconds: 30
log_file: "/var/log/printpilot-agent/agent.log"
log_level: "info"
spoolman_proxy_port: 8001
go2rtc_url: ""
camera_stream: ""
webrtc_timeout_seconds: 15
```

---

## Autenticación

El agente envía su `token` en el `Handshake` al conectar. El hub lo valida contra la fuente de verdad (el panel de PrintPilot). Si el token es inválido, el hub cierra con el código WebSocket **4001** y el agente **no reintenta en loop rápido**: espera 60s y vuelve a intentar, para que se recupere solo al corregir la config sin saturar el hub.

---

## Cámara en vivo (WebRTC P2P)

Con `go2rtc` corriendo en la misma máquina, el agente actúa como **intermediario de señalización** entre el panel y go2rtc (endpoint **WHEP**). El video fluye **P2P directo entre tu navegador y la impresora** — ni el hub ni el panel lo tocan.

- `camera_stream` define qué stream de go2rtc se sirve para la cámara "default" (vacío = usa el nombre `default`).
- Configurá en `go2rtc.yaml`: las credenciales **STUN/TURN** (sección `webrtc:`) y la fuente de la cámara (`on_demand: true` para que go2rtc solo capture mientras haya un viewer).
- El cierre de sesión lo gestiona go2rtc solo (WHEP es fire-and-forget): al cerrarse la conexión del navegador, libera el peer.

---

## Filamento (Spoolman)

El agente expone un **proxy local compatible con la API de Spoolman**. Moonraker reporta el consumo contra ese proxy y el agente lo reenvía al hub (que descuenta el peso del spool en el panel). El agente **no guarda inventario**: es un puente.

En `moonraker.conf`:

```ini
[spoolman]
server: http://localhost:8001
```

Contrato detallado: [`SPOOLMAN_COMPAT.md`](SPOOLMAN_COMPAT.md).

---

## Confiabilidad

- **Reconexión automática** a Moonraker y al hub, por separado, con backoff exponencial + jitter (500ms → 30s máx).
- Tras cada reconexión al hub se reenvía el `Handshake` automáticamente.
- Reportes de filamento pendientes (si el tunnel está caído) se encolan en un buffer acotado y se reintentan.

---

## Desarrollo

**Requisitos**: Go 1.25+.

```sh
make build          # build nativo → dist/printpilot-agent
make build-amd64    # linux/amd64
make build-386      # linux/386 (netbooks / 32 bits)
make build-arm      # linux/arm
make build-arm64    # linux/arm64 (Raspberry Pi)
make release        # los 4 binarios con la versión
make checksums      # .sha256 por binario (los verifica el instalador)
make test
```

### Estructura

```
printpilot-agent/
├── main.go
├── cmd/
│   ├── echoserver/      # servidor WSS de eco para pruebas locales
│   └── moonmock/        # Moonraker simulado para pruebas
├── internal/
│   ├── moonraker/       # cliente websocket de Moonraker
│   ├── tunnel/          # cliente WSS hacia el hub + reconexión
│   ├── bridge/          # reenvío entre Moonraker y el hub
│   ├── webrtc/          # señalización de cámara (WHEP con go2rtc)
│   ├── spoolmanproxy/   # proxy Spoolman (reporte de filamento)
│   ├── config/          # carga de config local
│   ├── backoff/         # backoff exponencial con jitter
│   └── runner/          # loop de conexión/reconexión genérico
├── deploy/
│   ├── install.sh              # instalador (install/update/delete/check)
│   └── printpilot-agent.service
├── config.example.yaml
└── Makefile
```

---

## Ecosistema

PrintPilot es un conjunto de repositorios que funcionan juntos:

| Repo | Rol |
|---|---|
| [printpilot-agent](https://github.com/Print-Pilot/printpilot-agent) | **Este**: el puente en la máquina de la impresora |
| [printpilot-hub](https://github.com/Print-Pilot/printpilot-hub) | Servidor central que gestiona las conexiones de los agentes |
| [printpilot-panel](https://github.com/Print-Pilot/printpilot-panel) | Dashboard web (Laravel + Filament) |
| [printpilot-protocol](https://github.com/Print-Pilot/printpilot-protocol) | Contrato de mensajes compartido entre los servicios |

---

## Estado del proyecto

En desarrollo activo (uso personal). La arquitectura central está estable; se está puliendo la experiencia de instalación y el streaming de cámara para producción.