# Spoolman compatibility — contrato a implementar en `spoolman-proxy`

> Documento de referencia (Fase 0). Subconjunto de la API de Spoolman que
> Moonraker usa de verdad, extraído del código fuente real:
> - `Arksine/moonraker` → `moonraker/components/spoolman.py` (commit master)
> - `Donkie/Spoolman` → `spoolman/api/v1/spool.py`, `spoolman/api/v1/models.py`, `spoolman/ws.py`
>
> No hace falta implementar el 100% de Spoolman, solo lo que Moonraker invoca.

---

## Cómo se conecta Moonraker

Moonraker lee de su config `[spoolman]` la opción `server:` (URL base, ej.
`http://localhost:8001`). A partir de esa URL deriva:

```
spoolman_url = "{scheme}://{host}/api"            # base HTTP
ws_url       = "{ws_scheme}://{host}/api/v1/spool" # websocket
```

Es decir: el agente (`spoolman-proxy`) debe escuchar en `{scheme}://{host}/api`
para HTTP y aceptar conexiones WS en `.../api/v1/spool`.

> El host/puerto los decide la config del agente (`spoolman_proxy_port`). La
> config de Moonraker apunta `[spoolman] server:` a ese puerto local.

---

## Endpoints HTTP que Moonraker invoca

Solo hay **dos** endpoints que importan para el reporte de consumo:

### 1. `GET /api/v1/spool/{id}`

Usado en `_check_spool_deleted()` (spoolman.py:216-231) al conectar, para
verificar que el spool activo todavía existe.

- **200** → devolver el objeto `Spool` (ver shape abajo).
- **404** → Moonraker interpreta que el spool fue borrado y limpia el activo
  (`set_active_spool(None)`).

### 2. `PUT /api/v1/spool/{id}/use` ← **el crítico**

Usado en `report_extrusion()` (spoolman.py:296-333). Moonraker acumula los
milímetros extruídos por spool activo y, cada `sync_rate` segundos (default 5),
envía:

```json
// PUT /api/v1/spool/7/use
{
  "use_length": 12.345        // mm extruídos desde el último reporte
}
```

También acepta `use_weight` (gramos), pero Moonraker **solo manda `use_length`**.

Respuesta esperada (200): el objeto `Spool` actualizado (ver shape abajo).
Si el spool no existe → 404 (Moonraker limpia el activo y descarta el reporte).

> **Para el proxy:** este `use_length` (en mm) + el `spool_id` (en la URL) es
> exactamente el dato que hay que convertir en `protocol.FilamentUsageReport`
> y reenviar por el tunnel. Si la densidad del filamento se conoce, se puede
> convertir mm → gramos; si no, se reenvía el largo y el panel decide.

---

## Websocket `WS /api/v1/spool`

Moonraker abre esta conexión en `_connect_websocket()` (spoolman.py:126-175) y
**no reporta consumo si no está conectada** (`report_extrusion` solo corre si
`ws_connected == true`, spoolman.py:297-298).

Requisitos mínimos del proxy:
- Aceptar la conexión WS y mantenerla abierta (reconexión automática la maneja
  Moonraker con backoff).
- Responder a los pings del cliente (Moonraker usa `on_ping`; el proxy debe
  responder pings WS para que la conexión no se considere caída).
- **No hay protocolo de suscripción** que Moonraker use: Spoolman implementa un
  `SubscriptionTree` con suscripción por pool, pero Moonraker se conecta directo
  a `/v1/spool` y solo **lee** mensajes. El proxy puede ignorar cualquier
  mensaje entrante de Moonraker.

Mensajes **salientes** opcionales que el proxy puede enviar para reflejar
estado (formato `Event` de Spoolman):

```json
{"type": "deleted", "resource": "spool", "date": "2026-08-20T00:00:00Z", "payload": {"id": 7}}
```

Moonraker reacciona (spoolman.py:201-209): si `resource == "spool"` y
`type == "deleted"` y `payload.id == spool_id`, limpia el spool activo.
Los eventos `added`/`updated` los ignora salvo ese caso.

---

## Shape del objeto `Spool` (model Spoolman)

Extraído de `models.py:285+`. Campos que importan (Moonraker no valida la
respuesta completa, solo usa el status code, pero devolver esto es lo correcto):

```json
{
  "id": 7,
  "registered": "2026-01-01T00:00:00Z",
  "filament": {
    "id": 3,
    "registered": "2026-01-01T00:00:00Z",
    "name": "PLA Black",
    "vendor": {"id": 1, "registered": "2026-01-01T00:00:00Z", "name": "Prusament", "extra": {}},
    "material": "PLA",
    "price": 24.9,
    "density": 1.24,
    "diameter": 1.75,
    "weight": 1000.0,
    "spool_weight": null,
    "settings": {},
    "extra": {}
  },
  "price": 24.9,
  "remaining_weight": 987.6,
  "initial_weight": 1000.0,
  "spool_weight": null,
  "used_weight": 12.4,
  "remaining_length": 420000.0,
  "used_length": 5123.4,
  "location": "Shelf A",
  "lot_nr": "52342",
  "comment": null,
  "extra": {},
  "archived": false
}
```

> No hace falta replicar este objeto completo; el proxy puede devolver un JSON
> mínimo con `{"id": N, ...}` que contenga `id` (y opcionalmente el resto).
> Moonraker solo usa el **status code** (200/404) y, para el WS, el `payload.id`.

---

## Cómo setear el spool activo (dirección hub → agente → Moonraker)

Moonraker expone el método JSON-RPC `printer.gcode.script` para G-code, pero
para el spool activo lo que importa es:

- Endpoint interno de Moonraker `POST /server/spoolman/spool_id` con
  `{"spool_id": N}` (o `null` para desactivar).
- También registra el remote method `spoolman_set_active_spool`.

En la práctica, para que `printpilot-agent` le comunique a Moonraker un cambio
de spool activo, alcanza con mandar por HTTP a Moonraker:
`POST http://localhost:{moonraker_port}/server/spoolman/spool_id` con body
`{"spool_id": N}` (autenticado con la API key si hace falta). Esto es lo que
habilita `protocol.SetActiveSpool` del hub → agente → Moonraker.

---

## Resumen implementable

| Dirección | Transporte | Endpoint | Body/Respuesta |
|---|---|---|---|
| Moonraker → proxy | HTTP GET | `/api/v1/spool/{id}` | 200/404, body Spool |
| Moonraker → proxy | HTTP PUT | `/api/v1/spool/{id}/use` | `{"use_length": mm}` → 200 Spool |
| Moonraker ↔ proxy | WS | `/api/v1/spool` | mantener abierta, responder pings |
| proxy → Moonraker | HTTP POST | Moonraker `/server/spoolman/spool_id` | `{"spool_id": N\|null}` |

El proxy convierte el `use_length` + `spool_id` del PUT en un
`protocol.FilamentUsageReport` y lo manda por el tunnel. Y al recibir un
`protocol.SetActiveSpool` del hub, hace el POST a Moonraker para setear el
spool activo.
