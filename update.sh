#!/usr/bin/env bash
# ARTEX 更新指令碼：① Docker 更新（拉新映像重建）  ② 本地編譯更新（重建二進位制）
# 與 install.sh 對應：install 負責首次落地，update 負責升級到新版本。
# DB 遷移無需手動執行——artex 每次啟動都會冪等重跑 schema.sql（含 ADD COLUMN/CREATE
# INDEX IF NOT EXISTS），所以“重啟即遷移”。資料（pgdata 卷、./data、./skills）不受影響。
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── 可選：同步儲存庫到最新程式碼（compose/指令碼/本地編譯原始碼都靠它更新）───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "非 git 工作副本，跳過 git pull"; return; }
  [ "$(ask '拉取最新程式碼 (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull 未能快進（本地有改動或分支分叉）——請手動處理後重試，本次沿用當前程式碼"
  fi
}

# ── ① Docker 更新 ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "未檢測到 docker / docker compose，請先用 ./install.sh 安裝部署"
  [ -f .env ] || die "未找到 .env，請先執行 ./install.sh 完成首次部署"

  # 可選：升級到指定版本 tag（不填則沿用 .env 中的 ARTEX_TAG，預設為 latest）
  local tag; tag="$(ask '目標映像 tag（回車沿用 .env / latest）' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "已將 ARTEX_TAG 設為 ${tag}"
  fi

  # 只動 artex：postgres 是固定的 16-alpine，不需要跟著升級（拉它純屬浪費頻寬，
  # 且大版本變動還會有相容風險）。artex 聲明瞭 depends_on postgres，所以帶服務名
  # up 時若 pg 沒起會自動拉起，已在跑的則原樣保留、不重建。
  info "拉取新映像（僅 artex）…"
  docker compose pull artex
  info "重建並啟動（artex 重啟時自動遷移 schema）…"
  docker compose up -d artex
  ok "更新完成 → http://localhost:8787"
  info "檢視日誌：docker compose logs -f artex"
  info "清理舊映像（可選）：docker image prune -f"
}

# ── ② 本地編譯更新 ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "未檢測到 Go（>=1.26）：https://go.dev/dl/"
  [ -f config.json ] || warn "未找到 config.json——若首次部署請改用 ./install.sh"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "重建前端靜態產物…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "重新編譯內嵌單二進位制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "未檢測到 npm：編譯**不內嵌前端**的後端（前端需另跑 npm run dev）"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "編譯完成 → ./artex"
  warn "請重啟正在執行的 artex 程序以生效（重啟時會自動遷移 schema）"
}

echo "=============================="
echo "  ARTEX 更新"
echo "  1) Docker 更新（拉新映像重建）"
echo "  2) 本地更新（go 重新編譯）"
echo "=============================="
case "$(ask '選擇' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "無效選擇" ;;
esac
