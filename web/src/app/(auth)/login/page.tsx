"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // 滾動到條款底部（含無需滾動即可完整展示的情況）方可點選「同意」。
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // 開啟時重置，並處理內容本就不足一屏、無法觸發滾動的場景。
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // 已登入直接進主介面（靜態匯出下無 middleware 代勞這層跳轉）。
    const token = auth.getToken();
    if (token) {
      // localStorage 可能仍有憑據但 cookie 已丟失。先同步，再發起全新請求，
      // 避免服務端守衛或路由快取把跳轉送回仍處於 checking 狀態的登入頁。
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("無法連線到後端服務"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("請先閱讀並同意《使用須知》");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("使用者名稱或密碼錯誤");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        正在檢查登入狀態…
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">登入</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">歡迎回來，請輸入密碼以繼續使用 ARTEX</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">使用者名稱</Label>
              <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">密碼</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="請輸入密碼"
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                我已閱讀並同意
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  《使用須知》
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "登入中..." : "登入"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">ARTEX 使用須知與免責宣告</DialogTitle>
              <p className="text-xs text-muted-foreground">
                版本 v1.0 · 生效日期 2026-09-18 · 請在登入前完整閱讀以下全部條款
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              本《使用須知與免責宣告》（以下簡稱"本宣告"）是您與 ARTEX
              專案作者及貢獻者之間就使用本軟體所達成的約定。請您在使用前審慎閱讀、充分理解各條款內容，特別是以粗體或色塊標註的免責、責任限制及禁止性條款。
              <span className="font-medium text-foreground">
                {" "}
                一旦您下載、安裝、訪問或以任何方式使用本軟體，即視為您已閱讀、理解並同意接受本宣告的全部約束。
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                第一條 · 定義與開源許可
              </h4>
              <p className="pl-7">
                本軟體（ARTEX）是一款基於 GNU Affero General Public License
                v3.0（AGPL-3.0）釋出的開源程式。您可依據該協議自由使用、複製、修改和分發本軟體；但任何衍生作品（包括通過網路向第三方提供的線上服務）均須同樣以
                AGPL-3.0 協議開源並向使用者公開對應的完整原始碼。AGPL-3.0 完整條款以隨附的 LICENSE 檔案為準。
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                第二條 · 授權使用範圍
              </h4>
              <p className="pl-7">
                本軟體僅供個人學習、程式碼研究、安全技術原理探討，以及在您自行搭建的本地隔離環境中進行技術驗證之用，適用於學習、學術研究、程式碼審閱等非攻擊性、非破壞性用途。除本條明確許可的情形外，您不得將本軟體用於任何其他目的。
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                第三條 · 禁止行為
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  嚴禁對任何網站、線上服務、他人或第三方所有的聯網系統發起掃描、探測、利用或攻擊（無論是否已獲得授權、是否為您自有資產）；
                </li>
                <li>嚴禁將本軟體用於任何實際的滲透測試、攻防對抗、紅藍演練或生產環境；</li>
                <li>嚴禁將本軟體用於非法入侵、資料竊取、勒索、拒絕服務（DoS/DDoS）或任何破壞性、犯罪性活動；</li>
                <li>嚴禁移除、篡改或規避本軟體及其輸出中的任何版權、許可或安全提示資訊；</li>
                <li>嚴禁從事任何違反您所在國家或地區法律、法規及監管規定的行為。</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                第四條 · 智慧財產權
              </h4>
              <p className="pl-7">
                本軟體的著作權及相關智慧財產權歸專案作者及貢獻者所有，並在 AGPL-3.0
                協議約定的範圍內向您授予相應權利。除該協議明確授予的權利外，本宣告未以明示或默示方式授予您任何其他權利。
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                第五條 · 資料與隱私
              </h4>
              <p className="pl-7">
                本軟體為可自行部署的開源程式，作者不運營任何集中式服務、亦不會收集或上傳您的使用資料。您在使用過程中產生、處理或接觸的一切資料，均由您自行掌控並負責其合法性與安全性；因資料處理不當引發的任何後果由您自行承擔。
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                第六條 · 合規與法律責任
              </h4>
              <p className="pl-7">
                您應自行遵守所在國家或地區關於網路安全、資料安全與個人資訊保護、計算機犯罪等方面的全部法律法規（在中國大陸包括但不限於《網路安全法》《資料安全法》《個人資訊保護法》及相關司法解釋）。
                <span className="font-medium text-foreground">
                  {" "}
                  因您違反上述法律法規或本宣告約定而產生的一切法律責任與後果，均由您本人獨立承擔，與本軟體作者及貢獻者無關。
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                第七條 · 免責宣告與責任限制
              </h4>
              <p className="pl-7">
                本軟體按"現狀（AS IS）"與"現有（AS
                AVAILABLE）"狀態提供，不附帶任何明示或默示的擔保，包括但不限於對適銷性、特定用途適用性、準確性及不侵權的擔保。在適用法律允許的最大範圍內，本軟體作者及貢獻者不對因使用或無法使用本軟體（無論使用方式是否得當）而導致的任何直接、間接、偶然、特殊或後果性損失承擔責任，包括但不限於資料丟失、系統損壞、業務中斷、利潤損失或法律糾紛。
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                第八條 · 條款變更與最終解釋
              </h4>
              <p className="pl-7">
                作者有權根據法律法規或專案發展需要不時更新本宣告，更新後的版本將隨專案釋出並自公佈之日起生效；您繼續使用本軟體即視為接受修訂後的條款。在法律允許的範圍內，本宣告的最終解釋權歸專案作者所有。若本宣告任一條款被認定為無效，不影響其餘條款的效力。
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "您已瀏覽全部條款" : "請將條款滾動至底部後再確認"}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                我已閱讀並同意全部條款
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
