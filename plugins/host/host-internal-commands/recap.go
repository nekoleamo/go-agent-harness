package hostintcmd

// recap.go:/recap 本地会话速览(S-P0-5,对标 Hermes Agent /status 的本地 recap)。
//
// 与 host-session-summary(F3)的 LLM 概述**互补不重复**:
//   - 本命令只做**结构性统计**(轮数/耗时/工具分布/涉及文件/最近一问一答);
//   - 纯本地计算,**不调用任何模型**(测试断言无 ctx.llm 依赖路径);
//   - 不生成自然语言总结,不做语义判断。
//
// 数据源:ctx.sessions.Replay() 事件账本(与模型可见即已记录同源)。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// recapFileKeys 工具参数中可视为文件路径的键名(白名单,避免误取任意字符串)。
var recapFileKeys = map[string]bool{
	"path": true, "paths": true, "file": true, "files": true,
	"file_path": true, "filepath": true, "filename": true,
	"target": true, "source": true, "dest": true, "to": true, "from": true,
}

// truncateRunes 按 rune 截断并加省略号(不切坏多字节字符)。
func truncateRunes(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	out := []rune(s)
	return string(out[:limit]) + "…"
}

// humanDur 人类可读时长(秒级)。
func humanDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// recapArgsFiles 从工具调用参数(JSON 对象)提取路径白名单命中的值。
func recapArgsFiles(args string) []string {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return nil
	}
	var out []string
	for k, v := range m {
		if !recapFileKeys[k] {
			continue
		}
		switch val := v.(type) {
		case string:
			if val != "" {
				out = append(out, val)
			}
		case []any:
			for _, it := range val {
				if s, ok := it.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// cmdRecap 本地会话速览。args 预留(当前无参数)。
func (h *Host) cmdRecap(_ []string) (string, error) {
	var sess sdk.SessionLog
	if err := h.c.Inject("ctx.sessions", &sess); err != nil {
		return "", errString("ctx.sessions 未装配: " + err.Error())
	}
	evs := sess.Replay()

	var (
		userTurns, asstMsgs int
		firstTS, lastTS     time.Time
		lastUser, lastAsst  string
		model               string
		toolCalls           = map[string]int{}
		toolFails           = map[string]int{}
		fileTools           = map[string]map[string]bool{}
		callNames           = map[string]string{} // callID → tool name(结果计数用)
	)
	for i := range evs {
		ev := &evs[i]
		if !ev.TS.IsZero() {
			if firstTS.IsZero() || ev.TS.Before(firstTS) {
				firstTS = ev.TS
			}
			if ev.TS.After(lastTS) {
				lastTS = ev.TS
			}
		}
		switch p := ev.Payload.(type) {
		case sdk.UserMessage:
			userTurns++
			lastUser = p.Content
		case sdk.AssistantMessage:
			asstMsgs++
			if strings.TrimSpace(p.Content) != "" {
				lastAsst = p.Content
			}
		case sdk.ToolCallEvent:
			toolCalls[p.Name]++
			callNames[p.ID] = p.Name
			for _, f := range recapArgsFiles(p.Arguments) {
				if fileTools[f] == nil {
					fileTools[f] = map[string]bool{}
				}
				fileTools[f][p.Name] = true
			}
		case sdk.ToolResultEvent:
			if p.Error != "" {
				name := p.Name
				if name == "" {
					name = callNames[p.CallID]
				}
				if name == "" {
					name = "(未知)"
				}
				toolFails[name]++
			}
		case sdk.UsageEvent:
			if p.Model != "" {
				model = p.Model
			}
		}
	}

	if userTurns == 0 && asstMsgs == 0 {
		return "会话速览(本地统计,未调用模型)\n当前会话为空(无用户消息);对话后再试。", nil
	}

	var sb strings.Builder
	sb.WriteString("会话速览(本地统计,未调用模型)\n")
	if cs, err := h.cwdSessionsOrNil(); err == nil && cs != nil {
		fmt.Fprintf(&sb, "项目: %s\n会话: %s\n落盘: %s\n", cs.Current(), orDefault(sessionLabel(cs), "主会话"), cs.Path())
	}
	if model != "" {
		fmt.Fprintf(&sb, "模型: %s\n", model)
	}
	totalCalls, totalFails := 0, 0
	for _, n := range toolCalls {
		totalCalls += n
	}
	for _, n := range toolFails {
		totalFails += n
	}
	fmt.Fprintf(&sb, "轮次: %d 轮用户消息 / %d 条助手消息 / %d 次工具调用(失败 %d)\n",
		userTurns, asstMsgs, totalCalls, totalFails)
	if !firstTS.IsZero() && lastTS.After(firstTS) {
		fmt.Fprintf(&sb, "跨度: %s(首末事件)\n", humanDur(lastTS.Sub(firstTS)))
	}

	if totalCalls > 0 {
		type kv struct {
			name string
			n    int
		}
		rows := make([]kv, 0, len(toolCalls))
		for k, v := range toolCalls {
			rows = append(rows, kv{k, v})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].n != rows[j].n {
				return rows[i].n > rows[j].n
			}
			return rows[i].name < rows[j].name
		})
		const topN = 6
		parts := make([]string, 0, topN+1)
		for i, r := range rows {
			if i >= topN {
				parts = append(parts, fmt.Sprintf("…另 %d 种", len(rows)-topN))
				break
			}
			s := fmt.Sprintf("%s×%d", r.name, r.n)
			if f := toolFails[r.name]; f > 0 {
				s += fmt.Sprintf("(失败 %d)", f)
			}
			parts = append(parts, s)
		}
		sb.WriteString("工具 Top: " + strings.Join(parts, " · ") + "\n")
	}

	if len(fileTools) > 0 {
		files := make([]string, 0, len(fileTools))
		for k := range fileTools {
			files = append(files, k)
		}
		sort.Strings(files)
		fmt.Fprintf(&sb, "涉及文件(%d):\n", len(files))
		const fileN = 12
		for i, f := range files {
			if i >= fileN {
				fmt.Fprintf(&sb, "  …另 %d 个\n", len(files)-fileN)
				break
			}
			ops := make([]string, 0, len(fileTools[f]))
			for op := range fileTools[f] {
				ops = append(ops, op)
			}
			sort.Strings(ops)
			fmt.Fprintf(&sb, "  %s(%s)\n", f, strings.Join(ops, "/"))
		}
	}

	if lastUser != "" {
		fmt.Fprintf(&sb, "最近一问: %s\n", truncateRunes(lastUser, 100))
	}
	if lastAsst != "" {
		fmt.Fprintf(&sb, "最近一答: %s\n", truncateRunes(lastAsst, 140))
	}
	return sb.String(), nil
}

// cwdSessionsOrNil 取 ctx.cwdSessions(未装配返回 nil,不报错:recap 允许降级为纯统计)。
func (h *Host) cwdSessionsOrNil() (sdk.CwdSessions, error) {
	var cs sdk.CwdSessions
	if err := h.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil, err
	}
	return cs, nil
}
