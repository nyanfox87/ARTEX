#!/bin/sh
# ARTEX 守護啟動指令碼（Linux / macOS / Docker ENTRYPOINT）
#
# 用法：
#   ./start.sh                       前臺執行（Ctrl-C 停止）
#   nohup ./start.sh >artex.log 2>&1 &   後臺常駐
#   ./start.sh -addr :9000           額外引數原樣透傳給 artex
#
# 它只做一件事：把 artex 跑起來，程序退出後按退出碼決定要不要再拉起。
#
#   0      使用者正常停止        → 退出迴圈
#   75     程式請求重啟        → 立刻重跑（頁面點了"一鍵更新"或"回滾"）
#   其他   崩潰                → 退避後重跑（1→2→4…最多 60 秒）
#
# 刻意不在這裡做下載、SHA256 校驗或換裝：那些邏輯在 sh 和 bat 上要寫兩套，
# 而它們恰恰是最不能出錯的一環——一旦換上跑不起來的二進位制，本指令碼會忠實地
# 反覆拉起它，使用者只能上機器手工救。所以校驗/換裝全部留在 Go 裡（selfupdate 包），
# 由 artex 自己在啟動時完成，指令碼保持傻瓜化。
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 找不到執行檔 $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 轉發停止訊號給 artex 本體。
#
# Docker 下這是必需的：docker stop 只把 SIGTERM 發給 PID 1（也就是本指令碼），
# 不會發給子程序。不轉發的話 artex 收不到訊號、做不了優雅關閉，10 秒後被 SIGKILL
# 硬殺，正在跑的任務直接斷在半路。
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# 訊號會打斷 wait 並讓它返回 >128。此時子程序其實還在做優雅關閉，
	# 必須再 wait 一次才能拿到它真正的退出碼。
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] 已停止"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] 正常退出"
			exit 0
			;;
		"$RESTART_CODE")
			# 更新/回滾已就緒：重跑後 artex 會在啟動時完成換裝（見 selfupdate.Bootstrap）。
			echo "[artex] 請求重啟（應用新版本）…"
			delay=1
			;;
		*)
			echo "[artex] 異常退出 (code=$code)，${delay}s 後重啟" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
