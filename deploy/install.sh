#!/usr/bin/env bash
#
# printpilot-agent installer (install / update / delete)
#
# Uso:
#   sudo ./install.sh [command] [version]
#   sudo bash -s -- [command] [version]    (desde un pipe: curl | bash)
#
#   command: install (default) | update | delete | check
#   version: vX.Y.Z | latest (default)
#
# Después de instalar queda el CLI 'printpilot' en PATH:
#   printpilot status | doctor | printer new | config | log | update | uninstall
#
# Sin argumentos y en una terminal interactiva, muestra un MENÚ con flechas
# (↑/↓ + Enter) y colores para elegir la acción.
#
# Ejemplos:
#   # instalá la última versión
#   sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
#     | sudo bash -s -- install
#
#   # instalá una versión puntual (compatibilidad con la forma anterior)
#   sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
#     | sudo bash -s -- v0.1.0
#
#   # actualizá a la última y reiniciá el servicio
#   sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
#     | sudo bash -s -- update
#
#   # desinstalá (la config se respalda en /etc/printpilot-agent/config.yaml.bak.*)
#   sudo curl -sfL https://github.com/Print-Pilot/printpilot-agent/releases/latest/download/install.sh \
#     | sudo bash -s -- delete
#
# Antes de instalar, revisa que el sistema tenga lo necesario para que el
# agente sea útil (Moonraker/Klipper) y reporta las dependencias opcionales
# (Crowsnest, go2rtc, Go) con colores. Podés pasar variables de entorno para
# configurar sin prompts: PRINTPILOT_HUB_URL, PRINTPILOT_PRINTER_ID,
# PRINTPILOT_TOKEN, PRINTPILOT_MOONRAKER_URL, PRINTPILOT_GO2RTC_URL.
#
set -euo pipefail

# --- Configuración ----------------------------------------------------------
REPO="${PRINTPILOT_AGENT_REPO:-Print-Pilot/printpilot-agent}"
# Base de descarga (por defecto GitHub Releases). Se puede sobreescribir para
# usar un mirror/proxy o para tests locales.
DOWNLOAD_BASE="${PRINTPILOT_BASE_URL:-https://github.com/$REPO/releases/download}"
INSTALL_DIR="/opt/printpilot-agent"
CONFIG_DIR="/etc/printpilot-agent"
BINARY="$INSTALL_DIR/printpilot-agent"
VERSION_FILE="$INSTALL_DIR/VERSION"
SERVICE="printpilot-agent.service"
SERVICE_UNIT="/etc/systemd/system/$SERVICE"
RUN_USER="printpilot"
LOG_DIR="/var/log/printpilot-agent"

# --- Helpers -----------------------------------------------------------------
log()  { printf '\033[1;34m[printpilot]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m  [OK]\033[0m  %s\n' "$*"; }
warn() { printf '\033[33m  [!!]\033[0m  %s\n' "$*"; }
miss() { printf '\033[31m  [NO]\033[0m  %s\n' "$*"; }
die()  { printf '\033[31m[printpilot] ERROR: %s\033[0m\n' "$*" >&2; exit 1; }

# --- Interfaz interactiva (menús con flechas + colores) ----------------------
is_tty() { [ -t 0 ] && [ -t 1 ]; }

# menu_select <prompt> <opt1> [opt2] ... → imprime en stdout la opción elegida.
# Navegación con ↑/↓ y Enter. El renderizado (título, opciones, ✓) va a stderr
# para que stdout solo lleve la elección (usable en `var=$(menu_select ...)`).
menu_select() {
  local prompt="$1"
  shift
  local opts=("$@")
  local n=${#opts[@]}
  local cur=0 key first=1 i

  printf '\033[1;36m%s\033[0m\n' "$prompt" >&2
  printf '\033[?25l' >&2 # ocultar cursor

  while :; do
    if [ "$first" -eq 0 ]; then
      printf '\033[%dA' "$n" >&2 # volver al inicio de la lista para redibujar
    fi
    first=0
    for ((i = 0; i < n; i++)); do
      printf '\033[2K' >&2
      if [ "$i" -eq "$cur" ]; then
        printf '\033[1;7;36m > %s \033[0m\n' "${opts[$i]}" >&2
      else
        printf '\033[90m   %s\033[0m\n' "${opts[$i]}" >&2
      fi
    done

    read -rsn1 key || break
    if [ "$key" = $'\x1b' ]; then
      read -rsn2 -t 1 key 2>/dev/null || key=''
      case "$key" in
        '[A') cur=$(( (cur + n - 1) % n )) ;;
        '[B') cur=$(( (cur + 1) % n )) ;;
      esac
    elif [ -z "$key" ]; then
      break # Enter
    fi
  done

  printf '\033[?25h' >&2 # mostrar cursor
  # Confirmar: redibujar la lista con la opción elegida marcada.
  printf '\033[%dA' "$n" >&2
  for ((i = 0; i < n; i++)); do
    printf '\033[2K' >&2
    if [ "$i" -eq "$cur" ]; then
      printf '\033[1;32m ✓ %s\033[0m\n' "${opts[$i]}" >&2
    else
      printf '\n' >&2
    fi
  done
  printf '%s' "${opts[$cur]}"
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    if [ -f "$0" ] && command -v sudo >/dev/null 2>&1; then
      exec sudo bash "$0" "$@"
    fi
    die "Debe correr con sudo: sudo $0 $*"
  fi
}

have() { command -v "$1" >/dev/null 2>&1; }

# Cache del listado de unidades systemd: se consulta systemctl UNA sola vez
# (todas las dependencias lo reutilizan) y con timeout para no colgar el
# instalador si systemd está lento o no responde. El flag _unit_files_loaded
# evita re-consultar aunque el resultado sea vacío.
_unit_files=""
_unit_files_loaded=""

unit_exists() { # unit_exists <nombre>  (acepta "klipper" o "klipper.service")
  if [ -z "$_unit_files_loaded" ]; then
    _unit_files_loaded=1
    if command -v timeout >/dev/null 2>&1; then
      _unit_files="$(timeout 5 systemctl --no-pager list-unit-files 2>/dev/null || true)"
    else
      _unit_files="$(systemctl --no-pager list-unit-files 2>/dev/null || true)"
    fi
  fi
  printf '%s\n' "$_unit_files" | awk '{print $1}' | grep -qx "$1" \
    || printf '%s\n' "$_unit_files" | awk '{print $1}' | grep -qx "$1.service" \
    || [ -f "/etc/systemd/system/$1.service" ] \
    || [ -f "/usr/lib/systemd/system/$1.service" ] \
    || [ -f "/lib/systemd/system/$1.service" ]
}

# --- Detección de arquitectura ----------------------------------------------
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64" ;;
    i386|i486|i586|i686) echo "386" ;;
    armv7l|armv6l)  echo "arm" ;;
    aarch64|arm64)  echo "arm64" ;;
    *) die "Arquitectura no soportada: $(uname -m)" ;;
  esac
}

# --- Chequeo de dependencias ------------------------------------------------
# Detecta qué tiene el sistema y reporta en una tabla. Required = Moonraker
# (sin él el agente no hace nada), lo demás es informativo/opcional.
run_dep_checks() {
  # Feedback en vivo: cada dependencia imprime "Buscando X..." y el resultado
  # se agrega en la misma línea al terminar (✓ verde / ✗ rojo requerido / · gris opcional).
  check_start() { printf '  \033[90m·\033[0m Buscando \033[1m%-10s\033[0m...' "$1"; }
  check_ok()    { printf ' \033[1;32m✓\033[0m %s\n' "$1"; }
  check_skip()  { printf ' \033[90m·\033[0m %s\n' "$1"; }
  check_miss()  { printf ' \033[1;31m✗\033[0m %s\n' "$1"; }

  # --- Klipper (opcional, informativo) ---
  check_start "Klipper"
  klipper="no"; kdet="no encontrado"
  if unit_exists klipper; then
    klipper="si"; kdet="servicio"
  else
    for p in /home/*/klipper/klippy /home/*/printer_data/config /home/*/printer_data; do
      if [ -e "$p" ]; then klipper="si"; kdet="en $p"; break; fi
    done
  fi
  if [ "$klipper" = "si" ]; then check_ok "$kdet"; else check_skip "$kdet"; fi

  # --- Moonraker (requerido) ---
  check_start "Moonraker"
  moonraker="no"; mdet="no encontrado"
  if unit_exists moonraker; then
    moonraker="si"; mdet="servicio"
  elif { have curl && curl --connect-timeout 2 --max-time 3 -fsS http://localhost:7125/server/info >/dev/null 2>&1; } \
    || { have wget && wget -qO- --timeout=3 http://localhost:7125/server/info >/dev/null 2>&1; }; then
    moonraker="si"; mdet="responde en localhost:7125"
  fi
  if [ "$moonraker" = "si" ]; then check_ok "$mdet"; else check_miss "$mdet"; fi

  # --- Crowsnest (opcional, cámara) ---
  check_start "Crowsnest"
  crowsnest="no"; cdet="no encontrado (opcional)"
  if unit_exists crowsnest; then
    crowsnest="si"; cdet="servicio"
  elif have crowsnest; then
    crowsnest="si"; cdet="binario en PATH"
  else
    # Crowsnest se instala en el home del usuario (no está en PATH): buscarlo
    # por las rutas típicas (binario y config), igual que Klipper.
    for p in /home/*/crowsnest/crowsnest /home/*/crowsnest/crowsnest.conf \
             /home/*/printer_data/config/crowsnest.conf /usr/local/bin/crowsnest; do
      if [ -e "$p" ]; then crowsnest="si"; cdet="en $p"; break; fi
    done
  fi
  if [ "$crowsnest" = "si" ]; then check_ok "$cdet"; else check_skip "$cdet"; fi

  # --- go2rtc (opcional, cámara) ---
  check_start "go2rtc"
  go2rtc="no"; gdet="no encontrado (opcional)"
  if have go2rtc; then
    go2rtc="si"; gdet="binario en PATH"
  elif unit_exists go2rtc; then
    go2rtc="si"; gdet="servicio"
  elif { have curl && curl --connect-timeout 2 --max-time 3 -fsS http://localhost:1984/api/streams >/dev/null 2>&1; }; then
    go2rtc="si"; gdet="responde en localhost:1984"
  fi
  if [ "$go2rtc" = "si" ]; then check_ok "$gdet"; else check_skip "$gdet"; fi

  # --- Go (opcional, build desde fuente) ---
  check_start "Go"
  go="no"; go_ver=""
  if have go; then go="si"; go_ver="$(go version 2>/dev/null | awk '{print $3}')"; fi
  if [ "$go" = "si" ]; then check_ok "$go_ver"; else check_skip "no encontrado (solo build)"; fi

  # --- Descarga (requerido) ---
  check_start "Descarga"
  dl="no"
  if have curl; then dl="curl"; elif have wget; then dl="wget"; fi
  if [ "$dl" != "no" ]; then check_ok "$dl"; else check_miss "ni curl ni wget"; fi

  # --- systemd (requerido para el servicio) ---
  check_start "systemd"
  if command -v systemctl >/dev/null 2>&1; then check_ok "disponible"; else check_miss "no detectado (requerido)"; fi

  echo

  if [ "$moonraker" != "si" ]; then
    warn "No se detectó Moonraker: el agente no va a poder conectarse a la impresora."
    echo
  fi
  if [ "$dl" = "no" ]; then
    die "Necesitás curl o wget para descargar el binario."
  fi
}

# --- Resolución de versión ---------------------------------------------------
resolve_version() {
  local requested="${1:-latest}"
  if [ "$requested" != "latest" ]; then
    echo "$requested"
    return 0
  fi
  log "Consultando la última versión de $REPO..." >&2
  local api="https://api.github.com/repos/$REPO/releases/latest"
  local body=""
  if have curl; then
    body="$(curl -fsSL --max-time 10 "$api" 2>/dev/null || true)"
  else
    body="$(wget -qO- --timeout=10 "$api" 2>/dev/null || true)"
  fi

  local ver=""
  ver="$(printf '%s\n' "$body" | grep -m1 '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/' || true)"
  if [ -z "$ver" ]; then
    # Sin la API no sabemos el número de versión exacto. No abortamos: bajamos
    # desde la ruta "latest" de GitHub (que no usa la API) y avisamos.
    # OJO: log/warn van a stderr para no contaminar el stdout (la versión).
    warn "No se pudo consultar la última versión en api.github.com (red o rate-limit)." >&2
    warn "Se descargará desde la ruta 'releases/latest' sin verificar el número de versión." >&2
    echo "latest"
    return 0
  fi
  echo "$ver"
}

# --- Descarga del binario ----------------------------------------------------
download_binary() { # download_binary <version> <arch> <dest>
  local version="$1" arch="$2" dest="$3"
  local url
  if [ "$version" = "latest" ]; then
    # Sin número de versión (fallback cuando la API no responde): el alias
    # "latest" de GitHub no depende de la API.
    url="https://github.com/$REPO/releases/latest/download/printpilot-agent-linux-$arch"
  else
    url="$DOWNLOAD_BASE/$version/printpilot-agent-linux-$arch"
  fi
  log "Descargando $url"
  if have curl; then
    curl -fsSL "$url" -o "$dest" || return 1
  else
    wget -qO "$dest" "$url" || return 1
  fi
  chmod +x "$dest"
  # Verificación de checksum opcional (si el release publica el .sha256).
  if have curl; then
    if curl -fsSL "$url.sha256" -o "$dest.sha256" 2>/dev/null; then
      local expected got
      expected="$(awk '{print $1}' "$dest.sha256")"
      got="$(sha256sum "$dest" | awk '{print $1}')"
      if [ "$expected" != "$got" ]; then
        rm -f "$dest" "$dest.sha256"
        return 1
      fi
      rm -f "$dest.sha256"
      ok "Checksum SHA256 verificado"
    fi
  fi
}

# --- Generación de config -----------------------------------------------------
prompt_value() { # prompt_value <var> <label> <default>  → setea la var
  local var="$1" label="$2" default="$3"
  if [ -n "${!var:-}" ]; then return; fi
  if is_tty; then
    printf '\033[1;36m%s\033[0m \033[90m[%s]\033[0m: ' "$label" "$default"
    local val
    read -r val || true
    if [ -n "$val" ]; then eval "$var=\$val"; fi
  fi
  if [ -z "${!var:-}" ]; then
    eval "$var=\$default"
  fi
}

write_config() {
  [ -f "$CONFIG_DIR/config.yaml" ] && return 0

  log "No hay config previa, generando $CONFIG_DIR/config.yaml"
  local default_hub="wss://hub.example.com/ws"
  local default_moonraker="ws://localhost:7125/websocket"
  local default_printer
  default_printer="$(hostname | tr '[:upper:]' '[:lower:]')"

  # Cámara (go2rtc): menú sí/no en modo interactivo.
  if [ -z "${PRINTPILOT_GO2RTC_URL:-}" ] && is_tty; then
    case "$(menu_select "¿Querés habilitar la cámara en vivo (go2rtc)?" \
      "Habilitar cámara (go2rtc)" \
      "No usar cámara")" in
      "Habilitar"*) PRINTPILOT_GO2RTC_URL="http://localhost:1984" ;;
      *) PRINTPILOT_GO2RTC_URL="" ;;
    esac
  fi

  prompt_value PRINTPILOT_HUB_URL "URL del hub (wss://...)" "$default_hub"
  prompt_value PRINTPILOT_PRINTER_ID "printer_id" "$default_printer"
  prompt_value PRINTPILOT_TOKEN "token del agente (Enter = vacío, solo dev)" ""
  prompt_value PRINTPILOT_MOONRAKER_URL "URL de Moonraker (websocket)" "$default_moonraker"
  prompt_value PRINTPILOT_GO2RTC_URL "URL de go2rtc (Enter = deshabilitado)" ""

  if [ -z "$PRINTPILOT_TOKEN" ]; then
    warn "Token vacío: la autenticación quedará deshabilitada (solo para desarrollo)."
  fi

  cat > "$CONFIG_DIR/config.yaml" <<EOF
# Config de printpilot-agent (generada por install.sh el $(date -u +'%Y-%m-%dT%H:%M:%SZ'))
hub_url: ${PRINTPILOT_HUB_URL}
token: "${PRINTPILOT_TOKEN}"
moonraker_url: ${PRINTPILOT_MOONRAKER_URL}
printer_id: "${PRINTPILOT_PRINTER_ID}"
heartbeat_interval_seconds: 30
log_file: "${LOG_DIR}/agent.log"
log_level: "info"
spoolman_proxy_port: 8001
go2rtc_url: "${PRINTPILOT_GO2RTC_URL}"
camera_stream: ""
webrtc_timeout_seconds: 15
EOF
  chown "$RUN_USER":"$RUN_USER" "$CONFIG_DIR/config.yaml"
  log "Config creada en $CONFIG_DIR/config.yaml (ajustá hub_url / token / printer_id si hace falta)"
}

# --- Service de systemd (embebido para que funcione vía pipe) ----------------
write_service() {
  cat > "$SERVICE_UNIT" <<'EOF'
[Unit]
Description=PrintPilot Agent (puente Moonraker <-> Hub)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=printpilot
Group=printpilot
WorkingDirectory=/opt/printpilot-agent
ExecStart=/opt/printpilot-agent/printpilot-agent --config /etc/printpilot-agent/config.yaml
Restart=always
RestartSec=5
# El agente solo hace conexiones salientes; no expone puertos.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/etc/printpilot-agent /var/log/printpilot-agent

[Install]
WantedBy=multi-user.target
EOF
}

# --- Comandos ----------------------------------------------------------------
cmd_install() { # cmd_install <version>
  require_root "$@"

  echo
  log "Chequeo de dependencias del sistema"
  run_dep_checks

  # Si falta Moonraker (requerido), ofrecer continuar/abortar en modo interactivo.
  if [ "$moonraker" != "si" ] && is_tty; then
    case "$(menu_select "Moonraker no está disponible. ¿Qué hacés?" \
      "Continuar de todos modos" \
      "Reintentar chequeo" \
      "Abortar")" in
      "Reintentar chequeo") cmd_install "$@" ;;
      "Abortar") die "Instalación abortada." ;;
    esac
  fi

  local version arch
  version="$(resolve_version "${1:-latest}")"
  [ -z "$version" ] && die "No se pudo resolver la versión (¿existe el release?)"
  arch="$(detect_arch)"
  log "Instalando $REPO $version (linux/$arch)"

  # Usuario y directorios.
  if ! id "$RUN_USER" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$RUN_USER" 2>/dev/null \
      || useradd --system --shell /bin/false "$RUN_USER"
    log "Usuario creado: $RUN_USER"
  fi
  mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$LOG_DIR"

  # Binario.
  if ! download_binary "$version" "$arch" "$BINARY.tmp"; then
    rm -f "$BINARY.tmp"
    die "No se pudo descargar el binario de $version. Verificá el nombre del asset (printpilot-agent-linux-$arch)."
  fi
  mv -f "$BINARY.tmp" "$BINARY"
  echo "$version" > "$VERSION_FILE"
  chown -R "$RUN_USER":"$RUN_USER" "$INSTALL_DIR" "$LOG_DIR"
  ok "Binario instalado: $BINARY ($version)"

  # CLI 'printpilot' en PATH: el mismo binario, invocado vía symlink, actúa
  # como CLI de gestión (status / printer / config / doctor / update / uninstall).
  mkdir -p /usr/local/bin
  ln -sf "$BINARY" /usr/local/bin/printpilot
  ok "CLI instalado: printpilot (status, printer, config, doctor, update, uninstall)"

  # Config.
  write_config

  # Service.
  write_service
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null
  systemctl restart "$SERVICE"
  ok "Servicio arrancado: systemctl status $SERVICE"

  echo
  echo "printpilot-agent instalado."
  echo "  - Binario:   $BINARY"
  echo "  - Versión:   $version"
  echo "  - Config:    $CONFIG_DIR/config.yaml"
  echo "  - Logs:      journalctl -u $SERVICE -f"
  echo "  - Estado:    systemctl status $SERVICE"
  echo "  - CLI:       printpilot status | printer new | doctor | config | log | update | uninstall"
  if [ -z "$(sed -n 's/^token: *"\([^"]*\)"/\1/p' "$CONFIG_DIR/config.yaml" 2>/dev/null)" ]; then
    warn "Recordá completar 'token' en $CONFIG_DIR/config.yaml si el hub exige autenticación."
  fi
  if [ -z "$(sed -n 's/^printer_id: *"\([^"]*\)"/\1/p' "$CONFIG_DIR/config.yaml" 2>/dev/null)" ]; then
    warn "No hay printer_id seteado: corré 'sudo printpilot printer new' y pegá el id en el panel."
  fi
}

cmd_update() { # cmd_update <version>
  require_root "$@"

  [ -x "$BINARY" ] || die "El agente no está instalado. Corré 'install' primero."

  local current target
  current="$(cat "$VERSION_FILE" 2>/dev/null || echo desconocida)"
  target="$(resolve_version "${1:-latest}")"
  [ -z "$target" ] && die "No se pudo resolver la versión (¿existe el release?)"

  log "Actualizando de $current → $target"
  if [ "$current" = "$target" ] && [ "${PRINTPILOT_FORCE:-0}" != "1" ]; then
    log "Ya tenés la versión $target. Nada que hacer (PRINTPILOT_FORCE=1 para forzar)."
    return 0
  fi

  local arch
  arch="$(detect_arch)"
  if ! download_binary "$target" "$arch" "$BINARY.tmp"; then
    rm -f "$BINARY.tmp"
    die "No se pudo descargar $target. Verificá el nombre del asset."
  fi
  mv -f "$BINARY.tmp" "$BINARY"
  echo "$target" > "$VERSION_FILE"
  chown "$RUN_USER":"$RUN_USER" "$BINARY"

  systemctl daemon-reload
  systemctl restart "$SERVICE"
  ok "Actualizado a $target y servicio reiniciado."
}

cmd_delete() {
  require_root "$@"

  if ! systemctl list-unit-files 2>/dev/null | grep -qx "$SERVICE" && [ ! -d "$INSTALL_DIR" ]; then
    log "No hay nada instalado (no existe el servicio ni $INSTALL_DIR)."
    return 0
  fi

  log "Deteniendo y removiendo el servicio"
  systemctl stop "$SERVICE" 2>/dev/null || true
  systemctl disable "$SERVICE" 2>/dev/null || true
  rm -f "$SERVICE_UNIT"
  systemctl daemon-reload

  # Symlink del CLI.
  rm -f /usr/local/bin/printpilot

  # Respaldo de la config (nunca borrar el token/URLs sin copia).
  if [ -f "$CONFIG_DIR/config.yaml" ]; then
    local backup
    backup="$CONFIG_DIR/config.yaml.bak.$(date +%s)"
    cp -a "$CONFIG_DIR/config.yaml" "$backup"
    log "Config respaldada en $backup"
  fi

  rm -rf "$INSTALL_DIR" "$CONFIG_DIR" "$LOG_DIR"

  if id "$RUN_USER" >/dev/null 2>&1; then
    userdel "$RUN_USER" 2>/dev/null || true
    log "Usuario eliminado: $RUN_USER"
  fi

  log "printpilot-agent desinstalado."
}

cmd_check() {
  echo
  log "Chequeo de dependencias del sistema"
  run_dep_checks
  if [ -x "$BINARY" ]; then
    ok "Agente instalado en $BINARY (versión $(cat "$VERSION_FILE" 2>/dev/null || echo '?'))."
  else
    warn "El agente no está instalado todavía."
  fi
}

usage() {
  sed -n '2,24p' "$0"
}

# --- Dispatch ----------------------------------------------------------------
main() {
  local command="${1:-}"

  # Sin argumentos y en terminal: menú interactivo con flechas.
  if [ -z "$command" ] && is_tty; then
    echo
    case "$(menu_select "¿Qué querés hacer con printpilot-agent?" \
      "Instalar" \
      "Actualizar" \
      "Desinstalar" \
      "Chequear dependencias" \
      "Salir")" in
      "Instalar") command="install" ;;
      "Actualizar") command="update" ;;
      "Desinstalar") command="delete" ;;
      "Chequear"*) command="check" ;;
      *) exit 0 ;;
    esac
  fi

  command="${command:-install}"
  shift || true

  case "$command" in
    -h|--help|help) usage; exit 0 ;;
    install) cmd_install "$@" ;;
    update)  cmd_update "$@" ;;
    delete)  cmd_delete "$@" ;;
    check)   cmd_check "$@" ;;
    v[0-9]*|latest) # compatibilidad: primer argumento = versión → install
      cmd_install "$command" ;;
    *) die "Comando desconocido: '$command' (usá install | update | delete | check)" ;;
  esac
}

main "$@"