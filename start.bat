@echo off
rem 控制台切 UTF-8，否則本檔案裡的中文在 GBK 終端下是亂碼。
chcp 65001 >nul 2>&1
rem ARTEX 守護啟動指令碼（Windows）
rem
rem 用法：
rem   start.bat                  前臺執行（Ctrl-C 停止）
rem   start.bat -addr :9000      額外引數原樣透傳給 artex
rem
rem 它只做一件事：把 artex.exe 跑起來，程序退出後按退出碼決定要不要再拉起。
rem
rem   0      使用者正常停止     -> 退出迴圈
rem   75     程式請求重啟     -> 立刻重跑（頁面點了"一鍵更新"或"回滾"）
rem   其他   崩潰             -> 退避後重跑（1->2->4…最多 60 秒）
rem
rem 下載、SHA256 校驗、換裝都不在這裡，全部由 artex 自己在啟動時完成
rem （selfupdate 包）。指令碼保持傻瓜化，詳見 start.sh 頂部的說明。

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] 找不到執行檔 %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] 正常退出
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem 更新/回滾已就緒：重跑後 artex 會在啟動時完成換裝。
	echo [artex] 請求重啟（應用新版本）…
	set /a delay=1
	goto loop
)

echo [artex] 異常退出 ^(code=!code!^)，!delay!s 後重啟 1>&2
rem timeout 在被重定向的控制台裡會失敗，用 ping 兜底（延時 N 秒需要 N+1 次）。
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
