#!/usr/bin/env bash
# setup_server.sh — интерактивная сборка и установка yastation-server.
# Запускай как обычный пользователь из корня репозитория (или откуда угодно):
#   bash scripts/setup_server.sh
# sudo вызывается изнутри только там, где реально нужно (systemd-юнит).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${ROOT_DIR}/bin"
BIN_PATH="${BIN_DIR}/yastation-server"
ENV_FILE="${ROOT_DIR}/.env"
CURRENT_USER="${SUDO_USER:-$(id -un)}"

green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
err()    { printf '\033[31mОШИБКА: %s\033[0m\n' "$*" >&2; }

ask() {
  local var_name="$1" prompt="$2" default_val="${3:-}" secret="${4:-no}"
  local prompt_display input
  if [[ -n "${default_val}" ]]; then
    prompt_display="${prompt} [${default_val}]: "
  else
    prompt_display="${prompt}: "
  fi
  if [[ "${secret}" == "yes" ]]; then
    read -rsp "${prompt_display}" input
    echo ""
  else
    read -rp "${prompt_display}" input
  fi
  [[ -z "${input}" && -n "${default_val}" ]] && input="${default_val}"
  printf -v "${var_name}" '%s' "${input}"
}

port_in_use() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltn "( sport = :$1 )" 2>/dev/null | grep -q ":$1"
  else
    (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && exec 3<&- 3>&-
  fi
}

echo ""
bold "╔════════════════════════════════════════╗"
bold "║   yastation-server — сборка и установка  ║"
bold "╚════════════════════════════════════════╝"
echo ""

if ! command -v go >/dev/null 2>&1; then
  err "не нашёл 'go' в PATH. Поставь Go 1.23+ и запусти скрипт снова."
  exit 1
fi
green "✔  $(go version)"

# ── Настройки сервера ────────────────────────────────────────────

echo ""
bold "── Настройки сервера ─────────────────────────────────────────"
ask PORT "Порт" "8737"
while ! [[ "${PORT}" =~ ^[0-9]+$ ]]; do
  yellow "  порт должен быть числом"
  ask PORT "Порт" "8737"
done
while port_in_use "${PORT}"; do
  yellow "  порт ${PORT} занят, пробую следующий..."
  PORT=$((PORT + 1))
done
green "✔  Использую порт: ${PORT}"

ask DOMAIN "Поддомен для Caddy (пусто — пропустить)" ""

DEFAULT_TOKEN_FILE="${HOME}/.config/yastation/tokens.json"
ask TOKEN_FILE "Файл токенов Яндекс-аккаунта" "${DEFAULT_TOKEN_FILE}"

echo ""
if command -v openssl >/dev/null 2>&1; then
  DEFAULT_HTTP_TOKEN="$(openssl rand -hex 32)"
else
  DEFAULT_HTTP_TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
fi
echo "Сгенерирован случайный токен для доступа к API — просто нажми Enter,"
echo "чтобы использовать его, или впиши свой."
ask HTTP_TOKEN "YASTATION_HTTP_TOKEN" "${DEFAULT_HTTP_TOKEN}"

echo ""
read -rp "Подключить свои команды из examples/commands.json? [y/N]: " use_custom

# ── .env ────────────────────────────────────────────────────────

WRITE_ENV=true
if [[ -f "${ENV_FILE}" ]]; then
  yellow "⚠  .env уже существует: ${ENV_FILE}"
  read -rp "   Перезаписать? [y/N]: " overwrite
  [[ "${overwrite,,}" != "y" ]] && WRITE_ENV=false
fi

if $WRITE_ENV; then
  {
    echo "# yastation-server — сгенерировано scripts/setup_server.sh"
    echo "# $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
    echo "YASTATION_HTTP_ADDR=:${PORT}"
    echo "YASTATION_HTTP_TOKEN=${HTTP_TOKEN}"
    echo "YASTATION_TOKEN_FILE=${TOKEN_FILE}"
    if [[ "${use_custom,,}" == "y" ]]; then
      echo "YASTATION_COMMANDS_FILE=${ROOT_DIR}/examples/commands.json"
    fi
  } >"${ENV_FILE}"
  chmod 600 "${ENV_FILE}"
  green "✔  .env записан (${ENV_FILE}, права 600)"
else
  echo "Оставляю существующий .env без изменений."
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
  PORT="${YASTATION_HTTP_ADDR#*:}"
  HTTP_TOKEN="${YASTATION_HTTP_TOKEN}"
  TOKEN_FILE="${YASTATION_TOKEN_FILE:-${DEFAULT_TOKEN_FILE}}"
fi

# ── Сборка ──────────────────────────────────────────────────────

echo ""
bold "── Сборка ─────────────────────────────────────────────────────"
mkdir -p "${BIN_DIR}"
(cd "${ROOT_DIR}" && go build -o "${BIN_PATH}" ./cmd/yastation-server)
green "✔  Собрано: ${BIN_PATH}"

# ── Авторизация в Яндексе, если ещё не пройдена ──────────────────

if [[ ! -f "${TOKEN_FILE}" ]]; then
  echo ""
  yellow "ℹ  Файл токенов ${TOKEN_FILE} не найден — нужна разовая QR-авторизация."
  read -rp "   Авторизоваться сейчас? [Y/n]: " do_auth
  if [[ "${do_auth,,}" != "n" ]]; then
    (cd "${ROOT_DIR}" && YASTATION_TOKEN_FILE="${TOKEN_FILE}" go run ./cmd/yastation-auth)
  else
    yellow "   Не забудь потом: YASTATION_TOKEN_FILE=${TOKEN_FILE} go run ./cmd/yastation-auth"
  fi
fi

# ── systemd, опционально ─────────────────────────────────────────

echo ""
read -rp "Установить как systemd-сервис? [Y/n]: " install_service
INSTALL_SERVICE=true
[[ "${install_service,,}" == "n" ]] && INSTALL_SERVICE=false

if $INSTALL_SERVICE; then
  SERVICE_NAME="yastation.service"
  SERVICE_PATH="/etc/systemd/system/${SERVICE_NAME}"
  CURRENT_GROUP="$(id -gn "${CURRENT_USER}")"

  SERVICE_CONTENT=$(cat <<SVCEOF
[Unit]
Description=yastation-server — управление Яндекс Станцией
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${CURRENT_USER}
Group=${CURRENT_GROUP}
WorkingDirectory=${ROOT_DIR}
EnvironmentFile=-${ENV_FILE}
ExecStart=${BIN_PATH}
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
SVCEOF
  )

  bold "── Устанавливаю systemd-сервис (${SERVICE_PATH})…"
  if [[ "${EUID}" -eq 0 ]]; then
    printf '%s\n' "${SERVICE_CONTENT}" >"${SERVICE_PATH}"
    chmod 644 "${SERVICE_PATH}"
    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}"
    systemctl restart "${SERVICE_NAME}"
  else
    if ! command -v sudo >/dev/null 2>&1; then
      err "нужен sudo, чтобы установить сервис"
      exit 1
    fi
    printf '%s\n' "${SERVICE_CONTENT}" | sudo tee "${SERVICE_PATH}" >/dev/null
    sudo chmod 644 "${SERVICE_PATH}"
    sudo systemctl daemon-reload
    sudo systemctl enable "${SERVICE_NAME}"
    sudo systemctl restart "${SERVICE_NAME}"
  fi

  echo ""
  green "✔  Сервис установлен и запущен: ${SERVICE_NAME}"
  echo "   Статус: sudo systemctl status ${SERVICE_NAME}"
  echo "   Логи:   sudo journalctl -u ${SERVICE_NAME} -f"
fi

# ── Проверка и Caddyfile ─────────────────────────────────────────

echo ""
bold "── Проверка ───────────────────────────────────────────────────"
echo "  curl -X POST localhost:${PORT}/command \\"
echo "    -H \"Authorization: Bearer ${HTTP_TOKEN}\" \\"
echo "    -d '{\"text\": \"привет с сервера\"}'"

if [[ -n "${DOMAIN}" ]]; then
  echo ""
  bold "── Caddyfile ─────────────────────────────────────────────────"
  echo "  ${DOMAIN} {"
  echo "      reverse_proxy localhost:${PORT}"
  echo "  }"
  echo ""
  echo "  Перезапуск: sudo systemctl reload caddy"
fi

echo ""
bold "── Быстрый запуск без systemd ───────────────────────────────"
echo "  set -a && source ${ENV_FILE} && set +a && ${BIN_PATH}"
echo ""
