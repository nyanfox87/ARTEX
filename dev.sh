#!/usr/bin/env bash
# 開發模式：後端(:8787) + 流量代理(:8788) 與 前端 next dev(:5173) 一起跑。
# 前端 /api 反代到後端；Ctrl-C 一併退出。
#
# 單二進位制（前端內嵌）方式見 README「單二進位制」一節，不走這個指令碼。
set -euo pipefail
cd "$(dirname "$0")"

# 退出時結束本程序組內的所有子程序（後端 + 前端）。
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# 後端（普通 go run，不內嵌前端）；併發 work agent 數在「系統設定」裡配置。
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# 前端熱更新（Vite/Next dev server，/api 反代到 :8787）。
( cd web && npm run dev ) &

echo "[dev] 後端 :8787 / 代理 :8788 / 前端 http://localhost:5173  (Ctrl-C 退出)"
wait
