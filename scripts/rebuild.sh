#!/usr/bin/env bash
# rebuild.sh — git pull + пересборка yastation-server + перезапуск сервиса.
# Для использования после setup_server.sh, когда сервис уже установлен.
# Запускай из корня репозитория (или откуда угодно):
#   bash scripts/rebuild.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${ROOT_DIR}/bin"
BIN_PATH="${BIN_DIR}/yastation-server"
SERVICE_NAME="yastation.service"

green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
err()    { printf '\033[31mОШИБКА: %s\033[0m\n' "$*" >&2; }

run_sudo() {
  if [[ "${EUID}" -eq 0 ]]; then
    "$@"
  else
    if ! command -v sudo >/dev/null 2>&1; then
      err "нужен sudo для '$*'"
      exit 1
    fi
    sudo "$@"
  fi
}

cd "${ROOT_DIR}"

if ! command -v go >/dev/null 2>&1; then
  err "не нашёл 'go' в PATH."
  exit 1
fi

if [[ ! -d .git ]]; then
  err "${ROOT_DIR} — не git-репозиторий, обновлять нечего"
  exit 1
fi

echo ""
bold "── git pull ──────────────────────────────────────────────────"
if [[ -n "$(git status --porcelain)" ]]; then
  err "есть незакоммиченные изменения в ${ROOT_DIR} — сначала разберись (git status), потом повтори"
  exit 1
fi
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
BEFORE="$(git rev-parse HEAD)"
git pull --ff-only origin "${BRANCH}"
AFTER="$(git rev-parse HEAD)"
if [[ "${BEFORE}" == "${AFTER}" ]]; then
  green "✔  уже на последнем коммите: $(git log -1 --format='%h %s')"
else
  green "✔  обновлено: $(git log -1 --format='%h %s')"
fi

echo ""
bold "── Сборка ─────────────────────────────────────────────────────"
mkdir -p "${BIN_DIR}"
go build -o "${BIN_PATH}" ./cmd/yastation-server
green "✔  Собрано: ${BIN_PATH}"

echo ""
bold "── Перезапуск ────────────────────────────────────────────────"
if command -v systemctl >/dev/null 2>&1 && systemctl cat "${SERVICE_NAME}" >/dev/null 2>&1; then
  run_sudo systemctl restart "${SERVICE_NAME}"
  green "✔  ${SERVICE_NAME} перезапущен"
  echo "   Статус: sudo systemctl status ${SERVICE_NAME}"
  echo "   Логи:   sudo journalctl -u ${SERVICE_NAME} -f"
else
  yellow "⚠  systemd-сервис ${SERVICE_NAME} не найден (сервер не ставился через setup_server.sh с systemd)."
  yellow "   Перезапусти бинарник вручную: ${BIN_PATH}"
fi
