#!/usr/bin/env bash
#
# Instala printpilot-agent como servicio systemd.
#
# Uso:
#   curl -sfL <URL-del-release>/install.sh | bash -s -- <version>
#
# o localmente:
#   ./install.sh v0.1.0
#
# Requiere: bash, systemd, curl o wget. Debe correrse con privilegios de root
# (sudo). Detecta la arquitectura y descarga el binario correcto de GitHub
# Releases, lo instala en /opt/printpilot-agent, crea el usuario printpilot y
# el service de systemd. Si no existe config, la crea desde la plantilla.
set -euo pipefail

REPO="${PRINTPILOT_AGENT_REPO:-Print-Pilot/printpilot-agent}"
VERSION="${1:-}"
INSTALL_DIR="/opt/printpilot-agent"
CONFIG_DIR="/etc/printpilot-agent"
BINARY="$INSTALL_DIR/printpilot-agent"
SERVICE="printpilot-agent.service"
USER="printpilot"

# --- Detectar arquitectura -----------------------------------------------
case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  i386|i486|i586|i686) ARCH="386" ;;
  armv7l|armv6l) ARCH="arm" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Arquitectura no soportada: $(uname -m)" >&2; exit 1 ;;
esac

# --- Descargar binario ----------------------------------------------------
if [ -z "$VERSION" ]; then
  echo "Indicá la versión: ./install.sh vX.Y.Z (o pasala como argumento)." >&2
  exit 1
fi

URL="https://github.com/$REPO/releases/download/$VERSION/printpilot-agent-linux-$ARCH"
echo "Descargando $URL ..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "$BINARY"
else
  wget -qO "$BINARY" "$URL"
fi
chmod +x "$BINARY"

# --- Usuario y directorios ------------------------------------------------
if ! id "$USER" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$USER"
fi
mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" /var/log/printpilot-agent
chown -R "$USER":"$USER" "$INSTALL_DIR" /var/log/printpilot-agent

# --- Config ----------------------------------------------------------------
if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
  cp "$INSTALL_DIR/../config.example.yaml" "$CONFIG_DIR/config.yaml" 2>/dev/null \
    || echo "Ojo: no se pudo copiar plantilla de config; creala a mano en $CONFIG_DIR/config.yaml"
  chown "$USER":"$USER" "$CONFIG_DIR/config.yaml"
  echo "Config creada en $CONFIG_DIR/config.yaml — completá hub_url, printer_id y token."
fi

# --- Service ---------------------------------------------------------------
install -m 644 "$SERVICE" "/etc/systemd/system/$SERVICE"
systemctl daemon-reload
systemctl enable "$SERVICE"
systemctl start "$SERVICE"

echo
echo "printpilot-agent instalado como servicio."
echo "  - Binario:   $BINARY"
echo "  - Config:    $CONFIG_DIR/config.yaml"
echo "  - Logs:      journalctl -u $SERVICE -f"
echo "  - Estado:    systemctl status $SERVICE"
