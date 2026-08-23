# printpilot-agent — Plan

## Propósito
Proceso liviano en Go que corre **en la máquina conectada a la impresora** (netbook, Pi, etc). Es el puente entre Moonraker (local) y el hub (nube), sin exponer ningún puerto entrante.

## Responsabilidades
- Conectarse al websocket de Moonraker (`localhost:7125`).
- Abrir y mantener una conexión **saliente** WSS persistente hacia `printpilot-hub`.
- Reenviar mensajes en ambos sentidos, transformándolos al formato de `printpilot-protocol`.
- Autenticarse ante el hub con un token único por impresora.
- Reconexión automática con backoff si se cae la conexión (a Moonraker o al hub).
- Enviar heartbeats periódicos.
- Loguear localmente para debug (archivo de log simple, rotable).

## Fuera de alcance
- No decide lógica de negocio (eso vive en el hub/panel).
- No expone ningún puerto propio a la red local ni a internet.
- No guarda estado persistente más allá de config local (token, URL del hub).

## Requisitos técnicos
- Debe compilar para `GOOS=linux GOARCH=386` (netbooks/hardware viejo de 32 bits) además de `amd64`/`arm`.
- Binario único, sin dependencias externas en runtime.
- Footprint de memoria bajo (hardware limitado tipo Atom).
- Config vía archivo simple (YAML/TOML/JSON) — algo como `agent-config.yaml` con: `hub_url`, `token`, `moonraker_url`, `printer_id`.

## Estructura propuesta (borrador)
```
printpilot-agent/
├── main.go
├── internal/
│   ├── moonraker/      # cliente websocket de Moonraker
│   ├── tunnel/         # cliente WSS hacia el hub + reconexión
│   ├── bridge/         # lógica de reenvío entre ambos
│   └── config/         # carga de config local
├── config.example.yaml
└── README.md
```

## Fases de desarrollo

## Fase 0 — Setup del repo (previo a Fase 1)

- [x] Inicializar módulo Go: `go mod init github.com/Print-Pilot/printpilot-agent`
- [x] Fijar versión mínima de Go en `go.mod` (recomendado: la última estable al momento, compatible con `GOARCH=386`) — **go 1.25.2**
- [x] Crear estructura de carpetas base según el borrador del plan (`internal/moonraker`, `internal/tunnel`, `internal/bridge`, `internal/config`)
- [x] Elegir librería de WebSocket cliente — **decidido: `github.com/coder/websocket` (ex-nhooyr)**
- [x] Elegir librería de parseo de config — **decidido: `gopkg.in/yaml.v3`**
- [x] `.gitignore` básico (binarios, `agent-config.yaml` real, logs)
- [x] `Makefile` o script simple con targets: `build`, `build-386`, `build-arm`, `run`, `test`
- [x] CI mínimo (GitHub Actions) que al menos compile en los tres targets (`amd64`, `386`, `arm`) — no bloqueante para Fase 1, pero dejarlo anotado

**Definición de hecho (DoD):** `go build ./...` funciona en limpio y el repo tiene la estructura de carpetas acordada, aunque estén vacías.

---

## Fase 1 — Bridge mínimo (prueba de concepto)

Objetivo: conectar a Moonraker real y a un WSS de prueba, y ver mensajes fluyendo por consola. Sin auth, sin reconexión robusta, sin persistencia real. Esto es lo que te desbloquea para "empezar a probar cómo funciona el agente".

### 1.1 — Config mínima
- [x] Definir struct `Config` en `internal/config`: `HubURL`, `Token` (puede quedar vacío por ahora), `MoonrakerURL`, `PrinterID`
- [x] Función `LoadConfig(path string) (*Config, error)` que lea `agent-config.yaml`
- [x] Crear `config.example.yaml` con valores de ejemplo (`moonraker_url: ws://localhost:7125/websocket`, etc.)
- [x] Validación básica: si falta `moonraker_url`, error claro al arrancar (no panic silencioso)

**DoD:** `go run . --config config.example.yaml` levanta sin errores y loguea la config parseada (sin loguear el token en claro, aunque esté vacío — hábito a instalar desde ya).

### 1.2 — Cliente Moonraker (solo lectura)
- [x] Conectar al websocket de Moonraker en `MoonrakerURL`
- [x] Suscribirse a los objetos básicos de estado (`printer.objects.subscribe` con algo mínimo como `print_stats`, `extruder`, `heater_bed`) — usar la API JSON-RPC de Moonraker
- [x] Loguear por consola cada mensaje recibido (raw, sin transformar todavía)
- [x] Manejar el caso "Moonraker no está corriendo" sin crashear (log de error y salir limpio, la reconexión llega en Fase 3)

**DoD:** con Moonraker corriendo localmente (o en la netbook), el agente muestra por consola los updates de estado en tiempo real.

### 1.3 — Cliente WSS de prueba (hacia el hub)
- [x] Levantar un servidor WSS de eco simple (puede ser un script Go de 20 líneas, o `websocat` para pruebas manuales) — esto no es parte del binario final, es solo para probar — **implementado como `cmd/echoserver`**
- [x] El agente abre conexión saliente WSS hacia `HubURL`
- [x] Loguear por consola cada mensaje recibido desde ese servidor de eco
- [x] Enviar un mensaje simple de "hola, soy printer_id X" al conectar (sin formato de protocolo todavía, texto plano está bien acá)

**DoD:** con el servidor de eco corriendo, el agente se conecta, manda un mensaje y ves el eco por consola.

### 1.4 — Entry point
- [x] `main.go` arma ambas conexiones (Moonraker + WSS de prueba) en paralelo (goroutines) y las mantiene vivas
- [x] Manejo de `SIGINT`/`SIGTERM` para cerrar conexiones limpiamente (`ctx` + `context.WithCancel`)

**DoD (de toda la Fase 1):** corriendo el binario en la netbook (o en tu máquina de desarrollo) con Moonraker real y un WSS de eco, ves en consola: (a) los status updates de Moonraker, (b) la conexión al WSS y el eco de vuelta. Esto es lo mínimo para validar que el bridge "respira". ✅ verificado localmente con echoserver + Moonraker simulado

---

## Fase 2 — Bridge real

Objetivo: reemplazar el logueo crudo por mensajes tipados según `printpilot-protocol`, y que el flujo sea bidireccional de verdad (comandos del hub llegan a Moonraker).

> **Bloqueante externo:** esta fase depende de que `printpilot-protocol` tenga al menos los structs de `Handshake` y `StatusUpdate` definidos (ver "Próximos pasos" del plan de protocol). Si el protocolo no está listo, se puede avanzar en paralelo con structs provisorios en el propio repo del agente y migrarlos después.

### 2.1 — Integrar printpilot-protocol
- [x] Agregar `printpilot-protocol` como dependencia Go — **✅ integrado vía `replace` local (repo aún no publicado en GitHub)**
- [x] Reemplazar el logueo crudo de 1.2 por parseo real de las respuestas de Moonraker hacia `protocol.StatusUpdate` — **✅ `internal/moonraker` parsea `notify_status_update` → `Status`**
- [x] Armar `protocol.Handshake` con `printer_id`, `token` (placeholder), `protocol_version`, versión del agente — **✅ `internal/tunnel` envía `protocol.Handshake` en el envelope**

### 2.2 — Bridge Moonraker → Hub
- [x] En `internal/bridge`, la función que recibe updates de `moonraker/` los transforma a `protocol.StatusUpdate` y los envía por `tunnel/` — **✅ `bridge.onMoonrakerStatus`**
- [x] Mapear eventos puntuales de Moonraker (impresión iniciada/terminada/error) a `protocol.Event` — **✅ `bridge.detectEventTransition`: emite `print_started` (→printing), `print_finished` (→complete), `print_failed` (→error/cancelled) con metadata (filename, print_duration); tests en `bridge_test.go`**
- [x] Enviar `Heartbeat` cada N segundos (configurable, default razonable tipo 30s) — **✅ `bridge.HeartbeatLoop` + `heartbeat_interval_seconds` en config**

### 2.3 — Bridge Hub → Moonraker
- [x] `tunnel/` recibe mensajes del hub y los pasa a `bridge/` — **✅**
- [x] `bridge/` interpreta `protocol.Command` (pausar, reanudar, cancelar, gcode) y llama al método correspondiente de Moonraker vía `moonraker/` — **✅ pause/resume/cancel/gcode → JSON-RPC de Moonraker**
- [x] Responder con `protocol.Ack` (éxito/error) hacia el hub tras ejecutar el comando — **✅**

**DoD:** con `printpilot-hub` en su propia Fase 1 (WSS mínimo sin auth) corriendo, el agente manda `StatusUpdate` reales y puede recibir y ejecutar al menos un comando (`pause`) contra Moonraker real, con `Ack` de vuelta. ✅ **verificado end-to-end contra el Moonraker real de la netbook (192.168.0.15): pause y gcode M105 → Ack success:true**

---

## Fase 3 — Robustez

Objetivo: que el proceso no se caiga nunca por errores de red, y que sea observable vía logs.

### 3.1 — Reconexión con backoff
- [x] Backoff exponencial con jitter para reconexión a Moonraker (separado del backoff hacia el hub — son fallos independientes) — **`internal/backoff` + `internal/runner`, cada conexión con su propio `Run`**
- [x] Backoff exponencial con jitter para reconexión al hub
- [x] Límite superior de espera configurable (para no tardar minutos en reintentar tras una caída larga) — **`backoff.Config.Max` (default 30s)**
- [x] Re-enviar `Handshake` automáticamente tras cada reconexión al hub — **el handshake se envía dentro de `tunnel.Connect`, que el runner llama en cada reconexión**

### 3.2 — Manejo de errores sin crash
- [x] Auditar todos los `panic` potenciales (nil pointer en parseo JSON, cierres de canal, etc.) y convertirlos en errores manejados — **corregido nil-pointer en `tunnel.SendEnvelope` y `moonraker.writeJSON` (conn nil en arranque/reconexión); escrituras protegidas con mutex**
- [x] Recuperación (`recover()`) en el loop principal como red de seguridad, pero sin depender de eso como estrategia primaria — **`runner.safeRun`** (reveló el bug de nil-pointer, que se corrigió de raíz)

### 3.3 — Logging a archivo con rotación
- [x] Reemplazar/complementar el log a consola con log a archivo (ej. `lumberjack` para rotación, o rotación manual simple) — **`gopkg.in/natefinch/lumberjack.v2` (MaxSize 10MB, 5 backups, 28 días, compresión)**
- [x] Niveles de log básicos (info/warn/error) — no hace falta algo elaborado — **`log_level`: debug|info|warn|error**
- [x] Ruta de log configurable en `agent-config.yaml` — **`log_file`**

**DoD:** desconectar el wifi/apagar Moonraker a propósito durante una prueba de 10+ minutos y confirmar que el agente se recupera solo cuando todo vuelve, sin reiniciar el proceso manualmente, y que el log en archivo cuenta la historia completa de lo que pasó. ✅ **verificado: matando Moonraker y el hub por separado, el agente reintentó con backoff (784ms→1.6s→3.6s→...) y se reconectó solo al relanzarlos, sin reiniciar; el re-handshake al hub ocurre automáticamente; log a archivo con toda la historia. Verificado con `-race` sin panics ni data races.**

---

## Fase 4 — Producción ✅ (completada)

### 4.1 — Autenticación real
- [x] Enviar `Token` real en el `Handshake` (ya no placeholder) — **el token se lee de config y viaja en el Handshake**
- [x] Manejar rechazo de auth del hub (log claro + no reintentar en loop infinito rápido si el token es inválido — distinto de un problema de red transitorio) — **si el hub cierra con close code 4001, el agente detecta `CloseAuthRejected` y espera 60s fijos en vez de backoff agresivo**

### 4.2 — Empaquetado y release
- [x] Script de build cruzado para `linux/amd64`, `linux/386`, `linux/arm` (y `arm64` si aplica a algún Pi) — **targets `build-386`/`build-arm`/`build-arm64` + `make release` que compila los 4**
- [x] GitHub Actions que, al taggear una versión, genere binarios y los suba a GitHub Releases — **`.github/workflows/release.yml` (tag v*)**
- [x] Versionado del binario embebido (`go build -ldflags "-X main.version=..."`) para que el `Handshake` reporte la versión real — **VERSION vía `git describe` en el Makefile**

### 4.3 — Instalación como servicio
- [x] Script de instalación one-liner (estilo crowsnest) que descargue el binario correcto según arquitectura, lo instale y cree el archivo de config si no existe — **`deploy/install.sh` (detecta arch, descarga de GitHub Releases, crea usuario/config)**
- [x] Unit file de `systemd` (`printpilot-agent.service`) con `Restart=always` — **`deploy/printpilot-agent.service`**
- [x] Documentar en el README el proceso de instalación y desinstalación — **README "Instalación como servicio"**

**DoD:** instalar el agente en una netbook limpia con un solo comando, que quede corriendo como servicio, sobreviva a un reboot y se autentique correctamente contra el hub de producción. ✅ **verificado contra la netbook (192.168.0.15): instalado como servicio systemd, autentica contra el panel vía hub y publica status en Redis → dashboard**

---

## Resumen de próximos pasos inmediatos

1. Fase 0 completa (setup del repo) — esto es lo primero que le podés pasar al agente de código tal cual.
2. Fase 1 completa, sección por sección (1.1 → 1.2 → 1.3 → 1.4) — cada una tiene su propio DoD, así que se puede validar incrementalmente sin esperar a tener todo el bridge terminado.
3. Una vez que Fase 1 esté validada contra tu Moonraker real, recién ahí conviene empezar Fase 2, idealmente en paralelo con que `printpilot-protocol` tenga sus primeros structs.