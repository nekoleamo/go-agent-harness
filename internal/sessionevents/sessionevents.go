// Package sessionevents 会话事件 jsonl ↔ 类型化事件的编解码点。
// Payload 还原是硬要求:json 反序列化后 Payload 是 map[string]any,展示层与投影全靠
// 类型断言,不还原 ⇒ 重放 11179 事件投影 0 行(TUI 启动/切换会话历史不可见)。
// 两个消费方(host-session-log 的 Load 恢复、web 导出端点)共用本实现,避免各自漂移。
package sessionevents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// maxLineBytes 单行上限:web_fetch 1MB 正文经 JSON 转义膨胀可到 ~2MB(实测 1.9MB),
// 旧上限 1MB 会让有效事件行触发 token too long 拖垮整个 boot;仍超限按坏行容忍跳过。
const maxLineBytes = 16 * 1024 * 1024

// NormalizePayload 把 json 反序列化后的 map Payload 按 Kind 还原为具体类型。
func NormalizePayload(ev *sdk.SessionEvent) any {
	m, ok := ev.Payload.(map[string]any)
	if !ok {
		return ev.Payload // 非 map(空/string):原样
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m
	}
	switch ev.Kind {
	case sdk.EventUserMessage:
		var v sdk.UserMessage
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventAssistantChunk:
		var v sdk.LLMStreamEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventAssistantMessage:
		var v sdk.AssistantMessage
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventToolCall:
		var v sdk.ToolCallEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventToolResult:
		var v sdk.ToolResultEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventFileChange:
		// S-P1-1:改动审计带 patch 文本(可能数十 KB),落盘再回放必须还原为具体类型,
		// 否则 /diff 读回的是 map → 打不开
		var v sdk.FileChangeEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return m // 未识别/二次解析失败:保留 map(消费者自行容忍)
}

// ReadFile 只读读入会话 jsonl → 类型化事件(容忍空行/坏行,不中断)。
// 与 host-session-log.Load 的区别:这里**不做**尾部残行修复(修复属于写入方,
// 需要文件偏移与追加句柄);本函数面向导出/展示,纯读、无副作用。
func ReadFile(path string) ([]sdk.SessionEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sessionevents: open %s: %w", path, err)
	}
	defer f.Close()
	var out []sdk.SessionEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev sdk.SessionEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // 容忍坏行(旧格式/半写)
		}
		ev.Payload = NormalizePayload(&ev)
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("sessionevents: read %s: %w", path, err)
	}
	return out, nil
}
