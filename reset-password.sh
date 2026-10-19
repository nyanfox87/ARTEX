#!/usr/bin/env bash
# =============================================================================
# ARTEX 管理員密碼重置指令碼
#
# 登入使用者名稱固定為 ARTEX；密碼以 bcrypt 雜湊存放在資料庫 settings 表的
# auth.password_hash 鍵。本指令碼連上資料庫後，用 pgcrypto 在庫內生成 bcrypt 雜湊
# 並寫回該鍵——與後端登入校驗（golang.org/x/crypto/bcrypt）完全相容。
#
# 兩種部署：
#   local （預設）—— 宿主機直接用 psql 連資料庫。連線資訊按以下優先順序獲取：
#                    命令列引數 > --dsn/$ARTEX_PG_DSN > config.json 的 database.*
#   docker        —— 通過 `docker compose exec`（或 `docker exec`）在 postgres
#                    容器內執行 psql（compose 預設不對宿主暴露 5432，故走容器內）。
#
# 用法示例：
#   ./reset-password.sh                          # 本地，自動讀 config.json/環境，互動輸入新密碼
#   ./reset-password.sh -p 'NewPass!'            # 本地，直接給定新密碼
#   ./reset-password.sh --dsn postgres://u:p@h:5432/artex
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
#   ./reset-password.sh -m docker                # docker 部署（讀 .env 的 POSTGRES_*）
#   ./reset-password.sh -m docker -c pg容器名 --exec docker
#
# 安全：新密碼經環境變數 + psql \getenv 傳入（不進入程序 argv），並用 :'var'
# 自動轉義（防 SQL 注入）；資料庫密碼經 PGPASSWORD 傳遞，同樣不進 argv。
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker（空=自動判定）
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # docker 模式的 postgres 服務/容器名（預設 postgres）
EXEC_KIND=""       # compose | docker（docker 模式下用哪種 exec；空=自動）
NEWPASS=""
ASSUME_YES=0

die() { echo "錯誤：$*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- 引數解析 -------------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "未知引數：$1（-h 檢視用法）" ;;
  esac
done

# ---- 從 config.json 讀取 database.*（僅 local 模式、且未顯式給出連線時）-----
# 優先用 python3 解析（健壯）；缺 python3 時退回 grep（config.json 為規整分欄位）。
read_config_json() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# 支援直接給 dsn，或分欄位
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # 極簡後備：逐鍵 grep（值為字串或數字）
    local k
    for k in host port user password dbname sslmode; do
      local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
  local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# ---- 自動判定模式 ---------------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "部署模式：$MODE"

# ---- 採集新密碼 -----------------------------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "輸入新密碼（使用者名稱固定為 ARTEX）：" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "密碼不能為空"
  read -r -s -p "再次輸入以確認：" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "兩次輸入不一致"
fi
[[ -n "$NEWPASS" ]] || die "密碼不能為空"

# 通過環境變數把密碼交給 psql（\getenv 讀取，不進入 argv/ps）
export ARTEX_RESET_NEWPASS="$NEWPASS"

# 庫內生成 bcrypt 並 upsert；密碼用 :'newpw' 自動轉義。CREATE EXTENSION 冪等，
# 若資料庫角色無建擴充套件權限會在此報錯（提示見下方 run 的失敗分支）。
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw ARTEX_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ---- 執行 -----------------------------------------------------------------
if [[ "$MODE" == "local" ]]; then
  # 連線資訊優先順序：命令列 > --dsn/$ARTEX_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "從 $cfg 讀取資料庫配置"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "本機未找到 psql（請安裝 postgresql-client，或改用 -m docker）"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "缺少資料庫使用者（-U）或有效的 config.json/DSN"
    [[ -n "$DBNAME" ]] || die "缺少資料庫名（-d）或有效的 config.json/DSN"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "目標資料庫：$target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "確認在該庫重置 ARTEX 密碼？[y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "寫入失敗。若報 pgcrypto 權限/缺失，請用具備建擴充套件權限的角色，或先手動執行 CREATE EXTENSION pgcrypto。"
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "未找到 docker"
  CONTAINER="${CONTAINER:-postgres}"

  # 選擇 exec 方式：優先 docker compose exec（服務名），否則 docker exec（容器名）
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # 容器內的 psql 憑據：優先命令列，其次 .env 的 POSTGRES_*，再退回 compose 預設(artex)
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-artex}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "目標：容器 $CONTAINER 內 psql -U $DUSER -d $DNAME（exec=$EXEC_KIND）"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "確認在該容器資料庫重置 ARTEX 密碼？[y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
  fi

  # -e 只帶名字不帶值 → 從當前環境繼承，密碼不出現在 docker 命令 argv 裡。
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "寫入失敗。請確認容器名（-c）、資料庫帳號（.env 的 POSTGRES_*），以及角色有 pgcrypto 權限。"
  fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ 已重置 ARTEX 管理員密碼。請用使用者名稱 ARTEX + 新密碼登入（無需重啟服務）。"
