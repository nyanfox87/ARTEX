#!/usr/bin/env bash
# ARTEX 安裝指令碼：① 全部 Docker  ② 本地編譯執行
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── docker 環境檢測 / 自動安裝 ───────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "已檢測到 docker 與 docker compose"; return
  fi
  warn "未檢測到 docker / docker compose"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask '自動安裝 Docker? (y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Docker 安裝完成（使用者組變更需重新登入後免 sudo）"
      else
        die "請自行安裝 docker 後重試"
      fi ;;
    Darwin) die "macOS 請安裝 Docker Desktop：https://www.docker.com/products/docker-desktop/" ;;
    *)      die "請自行安裝 docker 後重試" ;;
  esac
}

# ── ① 全部 Docker ───────────────────────────────
install_docker(){
  ensure_docker
  umask 077
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Postgres 密碼（回車隨機生成）' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY（可留空，後續在 UI 配）' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok "已生成 .env（POSTGRES_PASSWORD 已設定）"
  else
    info "沿用已存在的 .env"
  fi
  info "拉取映像並啟動…"
  docker compose pull
  docker compose up -d
  ok "啟動完成 → http://localhost:8787"
  info "檢視日誌：docker compose logs -f artex"
}

# ── ② 本地編譯執行 ──────────────────────────────
install_local(){
  echo "資料庫安裝方式："
  echo "  1) 連線已有 PostgreSQL"
  echo "  2) 用 Docker 起一個 PostgreSQL（需要 docker）"
  case "$(ask '選擇' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres 密碼（回車隨機）' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask '資料庫地址' 127.0.0.1)"
      DB_PORT="$(ask '埠' 5432)"
      DB_USER="$(ask '帳號' artex)"
      DB_PASS="$(ask '密碼' '')"
      DB_NAME="$(ask '資料庫名' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # 生成 config.json
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST}",
    "port": ${DB_PORT},
    "user": "${DB_USER}",
    "password": "${DB_PASS}",
    "dbname": "${DB_NAME}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  ok "已生成 config.json"

  # go 環境檢查
  command -v go >/dev/null 2>&1 || die "未檢測到 Go，請先安裝 Go（>=1.26）：https://go.dev/dl/"
  ok "Go: $(go version)"

  # 內嵌前端需要 node 出靜態產物
  if command -v npm >/dev/null 2>&1; then
    info "構建前端靜態產物…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "編譯內嵌單二進位制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "未檢測到 npm：將編譯**不內嵌前端**的後端（前端需另跑 npm run dev）"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "編譯完成 → ./artex"

  info "啟動…（Ctrl-C 退出）"
  ./artex
}

echo "=============================="
echo "  ARTEX 安裝"
echo "  1) 全部 Docker 安裝"
echo "  2) 本地執行（go 編譯）"
echo "=============================="
case "$(ask '選擇' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "無效選擇" ;;
esac
