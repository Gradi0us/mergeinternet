#!/usr/bin/env bash
# Installs mi-server on a Linux VPS (Debian/Ubuntu tested layout).
# Run as root from a directory containing the mi-server binary:
#   sudo bash server-setup.sh
# Re-running upgrades the binary and keeps the existing key.
# The last lines are machine-readable (MI_KEY / MI_PORT / MI_PUBLIC_IP) for the Windows app.
# Optional env: PORT (51820), SUBNET (10.66.0.0/24), WAN (auto-detected).
set -euo pipefail

PORT="${PORT:-51820}"
SUBNET="${SUBNET:-10.66.0.0/24}"
WAN="${WAN:-$(ip route show default | awk '{print $5; exit}')}"
DIR=/opt/mergeinternet
SRC="$(cd "$(dirname "$0")" && pwd)"

[ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }
[ -x "$SRC/mi-server" ] || { echo "mi-server binary not found next to this script"; exit 1; }
[ -n "$WAN" ] || { echo "could not detect WAN interface; set WAN=eth0"; exit 1; }
echo "WAN=$WAN PORT=$PORT SUBNET=$SUBNET"

install -d "$DIR"
install -m 0755 "$SRC/mi-server" "$DIR/mi-server"

if [ ! -f "$DIR/server.json" ]; then
  KEY="$("$DIR/mi-server" genkey)"
  cat > "$DIR/server.json" <<EOF
{
  "listen": ":$PORT",
  "key": "$KEY",
  "mode": "aggregate",
  "reorder_ms": 60,
  "tun": { "name": "mi0", "address": "${SUBNET%.*/*}.1/24", "mtu": 1360 }
}
EOF
  chmod 600 "$DIR/server.json"
  echo "created $DIR/server.json"
fi

# Forwarding + larger UDP buffers (mi-server asks for 4 MiB).
cat > /etc/sysctl.d/99-mergeinternet.conf <<EOF
net.ipv4.ip_forward = 1
net.core.rmem_max = 8388608
net.core.wmem_max = 8388608
EOF
sysctl --system >/dev/null

add() { # add an iptables rule only if it is not there yet
  local table=$1; shift
  iptables -t "$table" -C "$@" 2>/dev/null || iptables -t "$table" -A "$@"
}
add nat    POSTROUTING -s "$SUBNET" -o "$WAN" -j MASQUERADE
add filter FORWARD -s "$SUBNET" -j ACCEPT
add filter FORWARD -d "$SUBNET" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
add mangle FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
add filter INPUT -p udp --dport "$PORT" -j ACCEPT
if command -v netfilter-persistent >/dev/null; then
  netfilter-persistent save
else
  echo "NOTE: install iptables-persistent to keep these rules after reboot"
fi
if command -v ufw >/dev/null && ufw status | grep -q active; then
  ufw allow "$PORT/udp"
  echo "NOTE: ufw is active; also set DEFAULT_FORWARD_POLICY=\"ACCEPT\" in /etc/default/ufw"
fi

cat > /etc/systemd/system/mi-server.service <<EOF
[Unit]
Description=mergeinternet bonding server
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$DIR/mi-server run -config $DIR/server.json
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now mi-server
systemctl restart mi-server
systemctl --no-pager status mi-server | head -n 5 || true

KEY="$(sed -n 's/.*"key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$DIR/server.json" | head -n 1)"
[ -n "$KEY" ] || { echo "could not read key from $DIR/server.json"; exit 1; }
PUBLIC_IP="$(curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
if ! printf '%s' "$PUBLIC_IP" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
  PUBLIC_IP="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1 || true)"
fi

echo
echo "MI_KEY=$KEY"
echo "MI_PORT=$PORT"
echo "MI_PUBLIC_IP=$PUBLIC_IP"
echo "MÃ KẾT NỐI: mi1:$PUBLIC_IP:$PORT:$KEY"
