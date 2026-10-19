import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

const AUTH_PAGES = ["/login", "/setup"];

export async function proxy(request: NextRequest) {
  // Mock demo：無真實登入，放行所有頁面（客戶端 auth 守衛也會放行）。
  if (process.env.NEXT_PUBLIC_MOCK === "1") return NextResponse.next();

  // Pages served by Next dev must pass the same gate as the embedded UI.
  // Only send credentials to a loopback backend; never follow redirects.
  // Remote dev backends remain usable when their optional gate is disabled.
  let gate: Response;
  try {
    const backend = new URL(process.env.AUTOPENTEST_API ?? "http://localhost:8787");
    if (!["http:", "https:"].includes(backend.protocol) || backend.username || backend.password) {
      return new NextResponse("認證後端地址配置無效", { status: 503 });
    }
    const local = ["localhost", "127.0.0.1", "[::1]"].includes(backend.hostname);
    gate = await fetch(new URL("/api/basic-auth/check", backend), {
      headers: local
        ? {
            cookie: request.headers.get("cookie") ?? "",
            authorization: request.headers.get("authorization") ?? "",
            "x-forwarded-proto": request.nextUrl.protocol.replace(":", ""),
          }
        : {},
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      signal: AbortSignal.timeout(5000),
    });
    if (!local && gate.status === 401) {
      return new NextResponse("遠端後端已啟用 HTTP Basic Auth，請通過內嵌前端訪問該服務", { status: 503 });
    }
  } catch {
    return new NextResponse("認證服務暫時不可用，請稍後重試", { status: 503 });
  }
  if (!gate.ok) {
    const headers = new Headers({ "Cache-Control": "no-store", "Content-Type": "text/plain; charset=utf-8" });
    const challenge = gate.headers.get("www-authenticate");
    if (challenge) headers.set("WWW-Authenticate", challenge);
    return new NextResponse(gate.status === 401 ? "請完成 HTTP Basic Auth 驗證後重新整理頁面" : "認證服務暫時不可用", {
      status: gate.status,
      headers,
    });
  }
  const finish = (response: NextResponse) => {
    const cookie = gate.headers.get("set-cookie");
    if (cookie) response.headers.append("Set-Cookie", cookie);
    response.headers.set("Cache-Control", "no-store");
    return response;
  };

  const { pathname } = request.nextUrl;
  const token = request.cookies.get("artex_token")?.value;
  const isAuthPage = AUTH_PAGES.some((p) => pathname === p || pathname.startsWith(`${p}/`));

  // 未登入 → 跳轉登入頁
  if (!token && !isAuthPage) {
    return finish(NextResponse.redirect(new URL("/login", request.url)));
  }

  // 已登入時訪問登入/初始化頁 → 跳轉主介面
  if (token && isAuthPage) {
    return finish(NextResponse.redirect(new URL("/function/tasks", request.url)));
  }

  return finish(NextResponse.next());
}

export const config = {
  // 跳過 Next.js 內部路由、API 路由、favicon 及 public/ 下的靜態檔案（含圖片、字型等）
  matcher: [
    "/((?!_next/static|_next/image|favicon\\.ico|api/|.*\\.(?:png|jpg|jpeg|gif|webp|svg|ico|woff2?|ttf|otf)$).*)",
  ],
};
