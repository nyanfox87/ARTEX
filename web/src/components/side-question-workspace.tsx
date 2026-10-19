"use client";

import { type ReactNode, useEffect, useRef, useState } from "react";

import { ArrowUpIcon, MessageCircleQuestionIcon, SquareIcon, Trash2Icon, XIcon } from "lucide-react";

import { Markdown } from "@/components/markdown";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Drawer, DrawerContent, DrawerDescription, DrawerHeader, DrawerTitle } from "@/components/ui/drawer";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupTextarea } from "@/components/ui/input-group";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { Skeleton } from "@/components/ui/skeleton";
import { useIsMobile } from "@/hooks/use-mobile";
import type { SideQuestions } from "@/hooks/use-side-questions";
import { cn } from "@/lib/utils";

type ComposerLayout = "inline" | "stacked";

const preparationLabels = {
  preparing: "正在準備上下文…",
  summarizing_history: "正在整理早期旁路問答…",
  compressing_snapshot: "正在壓縮旁路上下文副本…",
  retrying: "模型上下文超限，正在縮減後重試…",
  answering: "正在回答…",
};

export function SideQuestionButton({ side }: { side: SideQuestions }) {
  if (!side.enabled) return null;
  return (
    <Button variant="outline" size="sm" onClick={() => side.setOpen(true)} title="/btw 旁路提問">
      <MessageCircleQuestionIcon data-icon="inline-start" />
      旁路提問
    </Button>
  );
}

function SidePanel({
  side,
  label,
  composerLayout,
  suggestions,
  placeholder,
}: {
  side: SideQuestions;
  label: string;
  composerLayout: ComposerLayout;
  suggestions?: string[];
  placeholder?: string;
}) {
  const inlineComposer = composerLayout === "inline";
  const [confirm, setConfirm] = useState(false);
  const viewport = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const tail = side.items.at(-1);
  // biome-ignore lint/correctness/useExhaustiveDependencies: New cumulative text scrolls only readers who remain at the bottom.
  useEffect(() => {
    if (pinned.current && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [tail?.answer, tail?.id]);
  const status = { running: "回答中", completed: "已完成", failed: "失敗", cancelled: "已停止", interrupted: "已中斷" };
  return (
    <section className="flex h-full min-h-0 flex-col bg-background" aria-label="旁路提問面板">
      <div className="flex items-center gap-2 border-b p-3">
        <div className="min-w-0 flex-1">
          <p className="font-medium">
            旁路提問 <span className="text-muted-foreground">/btw</span>
          </p>
          <p className="truncate text-muted-foreground text-xs">{label}</p>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setConfirm(true)}
          disabled={!side.items.length || side.busy}
          aria-label="清空旁路歷史"
        >
          <Trash2Icon />
        </Button>
        <Button variant="ghost" size="icon-sm" onClick={() => side.setOpen(false)} aria-label="關閉旁路面板">
          <XIcon />
        </Button>
      </div>
      <div className="border-b px-3 py-2 text-muted-foreground text-xs">
        {side.snapshot ? (
          <>
            <p>{side.snapshot.model.model}</p>
            <p>上下文更新於 {new Date(side.snapshot.captured_at).toLocaleString()}</p>
          </>
        ) : (
          "主 Agent 首次執行後即可提問"
        )}
      </div>
      <div
        ref={viewport}
        onScroll={(event) => {
          const el = event.currentTarget;
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
        className="min-h-0 flex-1 overflow-y-auto p-3"
      >
        {side.nextCursor > 0 && (
          <Button variant="ghost" size="sm" onClick={() => void side.load(side.nextCursor)}>
            載入更早的旁路問答
          </Button>
        )}
        {side.loading && <Skeleton className="h-16 w-full" />}
        {!side.loading && side.items.length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyTitle>隨時問一個問題</EmptyTitle>
              <EmptyDescription>根據當前 Agent 的上下文回答，主任務繼續執行。</EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        <div className="flex flex-col gap-5">
          {side.items.map((item) => (
            <article key={item.id} className="flex min-w-0 flex-col gap-2">
              <div className="whitespace-pre-wrap break-words rounded-lg bg-muted p-3 text-sm">{item.question}</div>
              <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                <Badge variant="secondary">{status[item.status]}</Badge>
                <span className="truncate">{item.model.model}</span>
                <time dateTime={item.snapshot_at} title={new Date(item.snapshot_at).toLocaleString()}>
                  上下文 {new Date(item.snapshot_at).toLocaleTimeString()}
                </time>
              </div>
              {item.context?.estimated_input_tokens != null && (
                <p className="text-muted-foreground text-xs">
                  最近 {item.context.recent_exchanges} 組問答原文
                  {item.context.history_summarized && " · 含早期問答摘要"}
                  {item.context.snapshot_summarized && " · 使用主上下文摘要"}
                </p>
              )}
              {item.answer && <Markdown text={item.answer} />}
              {!item.answer && item.status === "running" && (
                <p role="status" className="text-muted-foreground text-sm">
                  {preparationLabels[item.context?.phase ?? "answering"]}
                </p>
              )}
              {item.error && (
                <Alert variant="destructive">
                  <AlertDescription>{item.error}</AlertDescription>
                </Alert>
              )}
            </article>
          ))}
        </div>
      </div>
      <div className="shrink-0 border-t p-3">
        {(side.error || side.snapshot?.reason) && (
          <Alert variant="destructive" className="mb-2">
            <AlertDescription>{side.error || side.snapshot?.reason}</AlertDescription>
          </Alert>
        )}
        <InputGroup className={inlineComposer ? "min-h-10" : "min-h-9"}>
          <InputGroupTextarea
            rows={1}
            className={cn("overflow-y-auto", inlineComposer ? "max-h-40 min-h-0" : "max-h-36 min-h-9")}
            aria-label="旁路問題"
            placeholder={placeholder ?? "詢問當前上下文…"}
            value={side.draft}
            maxLength={4000}
            disabled={side.busy}
            onChange={(event) => side.setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                void side.ask(side.draft);
              }
            }}
          />
          <InputGroupAddon align={inlineComposer ? "inline-end" : "block-end"}>
            {!inlineComposer && <span className="text-muted-foreground text-xs">獨立問答 · 無工具執行</span>}
            {side.running ? (
              <InputGroupButton
                className="ml-auto"
                variant="destructive"
                size="icon-xs"
                onClick={() => void side.stop()}
                aria-label="停止旁路回答"
              >
                <SquareIcon />
              </InputGroupButton>
            ) : (
              <InputGroupButton
                className="ml-auto"
                variant="default"
                size="icon-xs"
                onClick={() => void side.ask(side.draft)}
                disabled={side.busy || !side.draft.trim() || !side.snapshot?.available}
                aria-label="傳送旁路問題"
              >
                <ArrowUpIcon />
              </InputGroupButton>
            )}
          </InputGroupAddon>
        </InputGroup>
        {suggestions?.length ? (
          <fieldset className="mt-2 flex flex-wrap gap-1.5" aria-label="快捷問題">
            {suggestions.map((suggestion) => (
              <Button
                key={suggestion}
                variant="outline"
                size="sm"
                className="h-auto whitespace-normal text-left text-xs"
                disabled={side.busy || !side.snapshot?.available}
                onClick={() => void side.ask(suggestion)}
              >
                {suggestion}
              </Button>
            ))}
          </fieldset>
        ) : null}
      </div>
      {inlineComposer && (
        <div className="shrink-0 truncate px-3 pt-0.5 pb-1 text-muted-foreground text-xs">獨立問答 · 無工具執行</div>
      )}
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>清空旁路歷史？</AlertDialogTitle>
            <AlertDialogDescription>
              刪除當前 Agent 的旁路問答，並停止正在生成的旁路回答。主會話和上下文快照會保留。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => void side.clear()}>清空歷史</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

export function SideQuestionWorkspace({
  side,
  label,
  children,
  composerLayout = "stacked",
  suggestions,
  placeholder,
  className,
  mainClassName,
}: {
  side: SideQuestions;
  label: string;
  children: ReactNode;
  composerLayout?: ComposerLayout;
  suggestions?: string[];
  placeholder?: string;
  className?: string;
  mainClassName?: string;
}) {
  const mobile = useIsMobile();
  const desktopSideOpen = side.open && side.enabled && !mobile;
  return (
    <>
      {desktopSideOpen ? (
        <ResizablePanelGroup orientation="horizontal" className={cn("min-h-0 min-w-0 flex-1", className)}>
          <ResizablePanel
            id="main-conversation"
            minSize="35%"
            className={cn("flex min-h-0 min-w-0 flex-col", mainClassName)}
          >
            {children}
          </ResizablePanel>
          <ResizableHandle withHandle />
          <ResizablePanel id="side-question" defaultSize="38%" minSize="280px" maxSize="65%">
            <SidePanel
              side={side}
              label={label}
              composerLayout={composerLayout}
              suggestions={suggestions}
              placeholder={placeholder}
            />
          </ResizablePanel>
        </ResizablePanelGroup>
      ) : (
        <div className={cn("flex min-h-0 w-full min-w-0 flex-1 flex-col", className, mainClassName)}>{children}</div>
      )}
      <Drawer open={mobile && side.open && side.enabled} onOpenChange={side.setOpen}>
        <DrawerContent className="h-[85svh]">
          <DrawerHeader className="sr-only">
            <DrawerTitle>旁路提問</DrawerTitle>
            <DrawerDescription>{label} 的獨立問答</DrawerDescription>
          </DrawerHeader>
          <SidePanel
            side={side}
            label={label}
            composerLayout={composerLayout}
            suggestions={suggestions}
            placeholder={placeholder}
          />
        </DrawerContent>
      </Drawer>
    </>
  );
}
