#!/usr/bin/env bash
# deploy/setup.sh — Configura e atualiza o Argos na VM do GCP.
#
# Uso (a partir da raiz do repositório clonado):
#   sudo bash deploy/setup.sh --domain argos.meu-dominio.com
#
# Pré-requisito que este script NÃO gerencia (faça antes):
#   /etc/argos/env com os três tokens preenchidos:
#     sudo mkdir -p /etc/argos
#     sudo cp deploy/env.example /etc/argos/env
#     sudo nano /etc/argos/env
#
# Idempotente: pode ser rodado várias vezes sem efeitos colaterais.
# Ao re-rodar após um `git pull`, atualiza o binário e reinicia o serviço.

set -euo pipefail

# ---------------------------------------------------------------------------
# Variáveis de instalação
# ---------------------------------------------------------------------------

GO_VERSION="1.24.4"          # versão mínima; troque se precisar de outra
ARGOS_BIN="/usr/local/bin/argos"
ARGOS_WORK="/var/lib/argos"
ARGOS_CFG="/etc/argos/config.yaml"
ARGOS_ENV="/etc/argos/env"
ARGOS_SERVICE_DST="/etc/systemd/system/argos.service"

# Diretório do script → raiz do repositório
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

DOMAIN=""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

info()  { printf '\033[1;34m[setup]\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m[ok]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
die()   { printf '\033[1;31m[erro]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Argumentos
# ---------------------------------------------------------------------------

usage() {
  echo "Uso: sudo bash deploy/setup.sh --domain <domínio>"
  echo "  --domain  Domínio HTTPS apontado para esta VM (ex: argos.meu-dominio.com)"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case $1 in
    --domain) DOMAIN="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -z "$DOMAIN" ]] && { warn "--domain não informado."; usage; }
[[ "$EUID" -eq 0 ]] || die "Execute como root: sudo bash deploy/setup.sh --domain <domínio>"

# ---------------------------------------------------------------------------
# 1. Verificar segredos
# ---------------------------------------------------------------------------

info "Verificando segredos em $ARGOS_ENV..."
[[ -f "$ARGOS_ENV" ]] || die \
  "Arquivo de segredos ausente: $ARGOS_ENV\n" \
  "Crie-o antes:\n" \
  "  sudo mkdir -p /etc/argos\n" \
  "  sudo cp deploy/env.example /etc/argos/env\n" \
  "  sudo nano /etc/argos/env"

# shellcheck source=/dev/null
source "$ARGOS_ENV"
[[ -n "${GITHUB_TOKEN:-}" ]]            || die "GITHUB_TOKEN não definido em $ARGOS_ENV"
[[ -n "${TELEGRAM_BOT_TOKEN:-}" ]]      || die "TELEGRAM_BOT_TOKEN não definido em $ARGOS_ENV"
[[ -n "${TELEGRAM_WEBHOOK_SECRET:-}" ]] || die "TELEGRAM_WEBHOOK_SECRET não definido em $ARGOS_ENV"
ok "Segredos presentes"

# ---------------------------------------------------------------------------
# 2. Go
# ---------------------------------------------------------------------------

go_version_ok() {
  command -v go &>/dev/null || return 1
  local ver major minor
  ver=$(go version | awk '{print $3}' | sed 's/go//')
  IFS='.' read -r major minor _ <<< "$ver"
  [[ "$major" -gt 1 ]] || { [[ "$major" -eq 1 ]] && [[ "$minor" -ge 22 ]]; }
}

install_go() {
  info "Instalando Go $GO_VERSION..."
  local arch
  arch=$(uname -m)
  [[ "$arch" == "aarch64" ]] && arch="arm64" || arch="amd64"
  local tarball="go${GO_VERSION}.linux-${arch}.tar.gz"
  curl -fsSL "https://dl.google.com/go/${tarball}" -o "/tmp/${tarball}"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${tarball}"
  rm "/tmp/${tarball}"
  export PATH="/usr/local/go/bin:$PATH"
  ln -sf /usr/local/go/bin/go   /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  ok "Go $(go version) instalado"
}

if go_version_ok; then
  ok "Go $(go version | awk '{print $3}') já instalado"
else
  install_go
fi

# ---------------------------------------------------------------------------
# 3. cloudflared
# ---------------------------------------------------------------------------

if ! command -v cloudflared &>/dev/null; then
  info "Instalando cloudflared..."
  local_arch=$(uname -m)
  [[ "$local_arch" == "aarch64" ]] && cf_arch="arm64" || cf_arch="amd64"
  curl -fsSL \
    "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${cf_arch}" \
    -o /usr/local/bin/cloudflared
  chmod +x /usr/local/bin/cloudflared
  ok "cloudflared instalado"
else
  ok "cloudflared já instalado"
fi

# ---------------------------------------------------------------------------
# 4. Flutter (opcional — para /preview do redeagenda)
# ---------------------------------------------------------------------------

if ! command -v flutter &>/dev/null; then
  warn "flutter não encontrado — o comando /preview não funcionará."
  warn "Instale manualmente quando precisar: https://docs.flutter.dev/get-started/install/linux"
else
  ok "flutter encontrado"
fi

# ---------------------------------------------------------------------------
# 5. Caddy
# ---------------------------------------------------------------------------

if ! command -v caddy &>/dev/null; then
  info "Instalando Caddy..."
  apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | tee /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -q
  apt-get install -y caddy
  ok "Caddy instalado"
else
  ok "Caddy já instalado"
fi

# ---------------------------------------------------------------------------
# 6. Caddyfile
# ---------------------------------------------------------------------------

info "Configurando Caddy para $DOMAIN..."
cat > /etc/caddy/Caddyfile <<EOF
${DOMAIN} {
    reverse_proxy localhost:8080
}
EOF
systemctl enable caddy
systemctl reload caddy 2>/dev/null || systemctl restart caddy
ok "Caddy configurado → https://$DOMAIN"

# ---------------------------------------------------------------------------
# 7. Build do binário
# ---------------------------------------------------------------------------

info "Compilando Argos (CGO_ENABLED=0)..."
CGO_ENABLED=0 go build -o "$ARGOS_BIN" "$REPO_DIR/cmd/orchestrator"
chmod +x "$ARGOS_BIN"
ok "Binário: $ARGOS_BIN ($(du -sh "$ARGOS_BIN" | cut -f1))"

# ---------------------------------------------------------------------------
# 8. Diretório de trabalho e checkouts
# ---------------------------------------------------------------------------

mkdir -p "$ARGOS_WORK/checkouts"
ok "Diretório de trabalho: $ARGOS_WORK"

# ---------------------------------------------------------------------------
# 9. config.yaml (criado apenas se não existir)
# ---------------------------------------------------------------------------

if [[ -f "$ARGOS_CFG" ]]; then
  ok "config.yaml já existe — mantido sem alterações"
else
  info "Criando $ARGOS_CFG..."
  mkdir -p /etc/argos
  cat > "$ARGOS_CFG" <<EOF
db_path: "${ARGOS_WORK}/orchestrator.db"
poll_interval: "60s"
max_concurrent_tasks: 1
base_branch: "main"
agent_branch_prefix: "agent/issue-"
lock_lease: "30m"
log_level: "info"
log_format: "text"

workspace:
  base_dir: "${ARGOS_WORK}/checkouts"
  git_host: "github.com"
  clone_scheme: "https"

github:
  api_base: "https://api.github.com"
  token_env: "GITHUB_TOKEN"
  repos:
    redeagenda: { owner: "marciosw", name: "redeagenda" }

telegram:
  bot_token_env: "TELEGRAM_BOT_TOKEN"
  secret_token_env: "TELEGRAM_WEBHOOK_SECRET"
  webhook_url: "https://${DOMAIN}/telegram/webhook"
  allowed_chat_ids: []
  message_format: "html"

model:
  default: "claude-opus-4-5"
  context_window: 200000
  context_threshold: 0.65
  windows:
    claude-opus-4-5:  200000
    claude-opus-4-8:  1000000
    claude-sonnet-4-6: 1000000

preview:
  enabled: true
  port_range_start: 9000
  port_range_end: 9099
  timeout_minutes: 30
  startup_timeout_seconds: 60
  cloudflared_bin: "cloudflared"
EOF
  warn "PENDENTE: edite $ARGOS_CFG e preencha allowed_chat_ids"
  warn "  Para obter seu chat_id: envie /start para o bot e consulte:"
  warn "  curl https://api.telegram.org/bot\$TELEGRAM_BOT_TOKEN/getUpdates"
  ok "$ARGOS_CFG criado"
fi

# ---------------------------------------------------------------------------
# 10. Serviço systemd
# ---------------------------------------------------------------------------

info "Instalando serviço systemd..."
cp "$REPO_DIR/deploy/argos.service" "$ARGOS_SERVICE_DST"
systemctl daemon-reload
systemctl enable argos
systemctl restart argos
ok "Serviço argos (re)iniciado"

# Aguarda 3s e verifica se ficou em pé
sleep 3
if systemctl is-active --quiet argos; then
  ok "argos está rodando"
else
  warn "argos não está ativo — verifique:"
  warn "  journalctl -u argos -n 50 --no-pager"
fi

# ---------------------------------------------------------------------------
# Resumo
# ---------------------------------------------------------------------------

echo ""
printf '\033[1;32m╔══════════════════════════════════════════════╗\033[0m\n'
printf '\033[1;32m║  Argos instalado com sucesso!                ║\033[0m\n'
printf '\033[1;32m╚══════════════════════════════════════════════╝\033[0m\n'
echo ""
echo "  Binário   : $ARGOS_BIN"
echo "  Config    : $ARGOS_CFG"
echo "  Segredos  : $ARGOS_ENV"
echo "  Trabalho  : $ARGOS_WORK"
echo "  Webhook   : https://$DOMAIN/telegram/webhook"
echo ""
echo "  Logs      : journalctl -fu argos"
echo "  Status    : systemctl status argos"
echo "  Reiniciar : systemctl restart argos"
echo ""

if grep -q "allowed_chat_ids: \[\]" "$ARGOS_CFG" 2>/dev/null; then
  printf '\033[1;33m[pendente]\033[0m Preencha allowed_chat_ids em %s e reinicie:\n' "$ARGOS_CFG"
  echo "  sudo nano $ARGOS_CFG"
  echo "  sudo systemctl restart argos"
fi
