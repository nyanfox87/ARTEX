# syntax=docker/dockerfile:1
#
# 執行映像（不在映像裡編譯）：只裝常用工具，放入**預編譯好的 Linux 單二進位制**。
# 二進位制由 CI 的 binaries job 交叉編譯（純 Go、無 QEMU），按目標架構放在
# 構建上下文的 dist/<TARGETARCH>/artex。這樣多架構構建時 arm64 只需模擬 apt 層，
# 不再模擬 Next/Go 編譯，速度快得多。
#
# 本地手動構建映像時，先自行準備二進位制：
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# 常用工具：ripgrep / curl / vim，加一批 recon 常備件（按需增刪）。
# Node 從 NodeSource 裝 22.x，與前端構建使用的主版本一致。
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# 預裝 Playwright MCP 與 CLI（全域），執行時不再 npx 聯網下載。
# @playwright/mcp：browser MCP 直接 `npx @playwright/mcp`（已全域裝好，無需 -y/@latest）。
# @playwright/cli：提供 playwright-cli，裝完順帶 --help 驗證可執行。
# 再裝 playwright（提供瀏覽器管理），裝完用 --with-deps 預置 chromium 及其系統依賴，
# 這樣容器內 MCP/CLI 首次啟動即可用，不再聯網下載瀏覽器。
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 預編譯好的對應架構二進位制（dist/amd64/artex 或 dist/arm64/artex）
COPY dist/${TARGETARCH}/artex /app/artex
# 守護啟動指令碼：程序退出後按退出碼決定是否重新拉起，頁面一鍵更新靠它完成換裝。
# 它同時負責把 SIGTERM 轉發給 artex —— docker stop 只把訊號發給 PID 1，
# 不轉發的話 artex 收不到、做不了優雅關閉，10 秒後被 SIGKILL 硬殺。
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/（SQLite + jwt.key）持久化點
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
