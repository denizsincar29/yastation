#!/usr/bin/env bash
# setup_server.sh — deploy yastation-server on a VPS as a systemd service.
#
# What it does:
#   1. Builds cmd/yastation-server and installs the binary system-wide.
#   2. Picks a free TCP port (uses --port if free, otherwise the next
#      free one after it).
#   3. Generates a random bearer token for YASTATION_HTTP_TOKEN.
#   4. Writes a systemd unit that runs the server as a given (non-root)
#      user, and enables it.
#   5. Prints a Caddyfile block you can drop in to reverse-proxy a
#      subdomain to it.
#
# It does NOT run `yastation-auth` for you — Yandex login needs an
# interactive QR scan, so do that once as the target user before starting
# the service (see the printed instructions at the end).
#
# Usage:
#   sudo ./scripts/setup_server.sh --user deniz --domain alice.denizsincar.ru [--port 8737] [--repo /path/to/yastation]
#
# Flags:
#   --user    unix user the service runs as (required)
#   --domain  subdomain to print a Caddyfile block for (optional)
#   --port    preferred port, default 8737 (script picks the next free
#             one if this is taken)
#   --repo    path to a yastation checkout, default: this script's parent
#             directory (i.e. run it from inside the repo, as normal)

set -euo pipefail

PORT=8737
DOMAIN=""
SERVICE_USER=""
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
	echo "Использование: sudo $0 --user ИМЯ_ПОЛЬЗОВАТЕЛЯ [--domain поддомен] [--port 8737] [--repo /путь/к/yastation]" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--user)
		SERVICE_USER="$2"
		shift 2
		;;
	--domain)
		DOMAIN="$2"
		shift 2
		;;
	--port)
		PORT="$2"
		shift 2
		;;
	--repo)
		REPO_DIR="$2"
		shift 2
		;;
	-h | --help)
		usage
		;;
	*)
		echo "Неизвестный аргумент: $1" >&2
		usage
		;;
	esac
done

if [ -z "$SERVICE_USER" ]; then
	echo "Нужен --user (от чьего имени запускать сервис)" >&2
	usage
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "Запусти через sudo — нужно писать в /etc/systemd/system и /usr/local/bin" >&2
	exit 1
fi
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
	echo "Пользователь $SERVICE_USER не существует. Создай его (adduser $SERVICE_USER) и запусти снова." >&2
	exit 1
fi
if ! command -v go >/dev/null 2>&1; then
	echo "Не нашёл 'go' в PATH. Поставь Go 1.23+ и запусти снова." >&2
	exit 1
fi
if [ ! -f "$REPO_DIR/go.mod" ]; then
	echo "Не похоже на репозиторий yastation: $REPO_DIR/go.mod не найден. Передай верный --repo." >&2
	exit 1
fi

# --- 1. Build ---------------------------------------------------------

echo "Собираю yastation-server..."
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
(cd "$REPO_DIR" && GOTOOLCHAIN=local go build -o "$BUILD_DIR/yastation-server" ./cmd/yastation-server)

install -m 0755 "$BUILD_DIR/yastation-server" /usr/local/bin/yastation-server
echo "Установлено: /usr/local/bin/yastation-server"

# --- 2. Find a free port ------------------------------------------------

port_in_use() {
	# Try ss first (most distros), fall back to /dev/tcp probing.
	if command -v ss >/dev/null 2>&1; then
		ss -ltn "( sport = :$1 )" 2>/dev/null | grep -q ":$1"
	else
		(exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && exec 3<&- 3>&-
	fi
}

while port_in_use "$PORT"; do
	echo "Порт $PORT занят, пробую следующий..."
	PORT=$((PORT + 1))
done
echo "Использую порт: $PORT"

# --- 3. Generate a bearer token -----------------------------------------

if command -v openssl >/dev/null 2>&1; then
	HTTP_TOKEN="$(openssl rand -hex 32)"
else
	HTTP_TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
fi
echo "Сгенерирован токен для YASTATION_HTTP_TOKEN (полный вывод — ниже)"

# --- 4. systemd unit -----------------------------------------------------

USER_HOME="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
TOKEN_FILE="$USER_HOME/.config/yastation/tokens.json"
ENV_FILE="/etc/yastation/server.env"
UNIT_FILE="/etc/systemd/system/yastation.service"

mkdir -p /etc/yastation
umask 077
cat >"$ENV_FILE" <<EOF
YASTATION_HTTP_ADDR=:$PORT
YASTATION_HTTP_TOKEN=$HTTP_TOKEN
YASTATION_TOKEN_FILE=$TOKEN_FILE
EOF
chmod 600 "$ENV_FILE"
echo "Настройки записаны в $ENV_FILE (владелец root, права 600)"

cat >"$UNIT_FILE" <<EOF
[Unit]
Description=yastation HTTP backend (Yandex Station control)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
EnvironmentFile=$ENV_FILE
ExecStart=/usr/local/bin/yastation-server
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
echo "Юнит записан в $UNIT_FILE"

systemctl daemon-reload
systemctl enable yastation.service
echo "Сервис включён (enable), но пока не запущен — сначала нужна авторизация, см. ниже."

# --- 5. Final instructions ------------------------------------------------

echo
echo "===================================================================="
echo "Готово. Осталось:"
echo
echo "1) Авторизуйся под пользователем $SERVICE_USER (нужен интерактивный QR):"
echo "     sudo -u $SERVICE_USER -H env YASTATION_TOKEN_FILE=$TOKEN_FILE \\"
echo "       go run $REPO_DIR/cmd/yastation-auth"
echo
echo "2) Запусти сервис:"
echo "     sudo systemctl start yastation.service"
echo "     sudo systemctl status yastation.service"
echo
echo "3) Токен для запросов к API (сохрани, второй раз не покажу):"
echo "     $HTTP_TOKEN"
echo
echo "   Проверка:"
echo "     curl -X POST localhost:$PORT/command \\"
echo "       -H \"Authorization: Bearer $HTTP_TOKEN\" \\"
echo "       -d '{\"text\": \"привет с сервера\"}'"
echo
if [ -n "$DOMAIN" ]; then
	echo "4) Добавь в Caddyfile и перезапусти Caddy (systemctl reload caddy):"
	echo
	echo "   $DOMAIN {"
	echo "       reverse_proxy localhost:$PORT"
	echo "   }"
else
	echo "4) Если нужен домен — запусти скрипт с --domain поддомен.example,"
	echo "   и здесь появится готовый блок для Caddyfile."
fi
echo "===================================================================="
