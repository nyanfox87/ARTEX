package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa 是 norma v0.4.0 引入的「模型驅動上下文壓縮」機制,作為平臺實驗功能由使用者在
// 系統設定中開關。它與內建 compaction 互斥:noaadapter.Enable 是唯一入口,一次掛上
// 上下文接管器(Compactor)、Compress 工具與三段常駐提示詞,不呼叫 Enable 即為關閉
// (內建 compaction 照常工作)。開關由每個 agent 注入的 noaEnabledFn 解析,每 run 讀
// 一次,故切換隻影響之後啟動的 run,無需重建 agent。

// enableNoa 在解析器報告開啟時把 noa 接入 opts。archiveRoot 是壓縮原文的持久化基目錄
// (取全域 workDir,各 agent 統一落在 <workDir>/noa 下,不隨任務/意圖目錄分散),sessionID
// 命名其下的歸檔子目錄(全域唯一,故同一基目錄內不衝突)。
//
// noa 是實驗功能:接入失敗不得中斷真實任務。發生錯誤時經 onWarn 上報並回退內建壓縮。
// 啟用成功時清掉 opts.Compaction,避免 agentcore 因「兩個上下文管理器同時設定」告警。
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa 壓縮啟用失敗,回退內建壓縮:" + err.Error())
		}
		return
	}
	// Compactor 覆蓋 Compaction,但兩者並存時 agentcore 每次會告警;明確清掉。
	opts.Compaction = nil
}
