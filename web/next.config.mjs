import { fileURLToPath } from "node:url";

// 靜態匯出：`NEXT_EXPORT=1 next build` 產出純靜態目錄到 web/out，可直接丟進
// nginx web 根目錄執行。開發(next dev)不設該變數，保留 /api 反代與熱更新。
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo：整站走 mock，無後端，無需 /api 反代。
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // 避免父目錄的 lockfile 影響根目錄推斷及資源路徑生成。
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // 允許從區域網 IP 訪問 dev 資源（HMR），按需增刪。
  // dev 階段放開任意 IPv4 來源訪問 /_next/* 與 HMR（區域網 IP 變動也不受影響）。
  // 注意：Next 出於安全禁止裸 "*"，需用分段通配；"*.*.*.*" 匹配任意 IPv4。
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // 純靜態匯出：無 Node 執行時；圖片不經最佳化；每個路由產出 <route>/index.html。
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo：無後端，不需要 /api 反代。
          images: { unoptimized: true },
        }
      : {
          // 開發：把 /api/* 反代到 Go 後端（預設 :8787，可用 AUTOPENTEST_API 覆蓋）。
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
