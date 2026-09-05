// Package hostsystemprompt 提供 host-system-prompt 插件:ctx.systemPrompt 服务。
// 组装模型可见消息:固定引导 + 全局/项目指令(AGENTS.md)+ 注册片段 + 工具 schema 清单 + 历史。
// 指令注入对齐 pi 语义:全局 $GAH_HOME/AGENTS.md → 项目 <workspace>/AGENTS.md(后者优先,顺序即覆盖)。
package hostsystemprompt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-system-prompt。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-system-prompt" }

// Start 注册 ctx.systemPrompt 服务;加载全局/项目指令文件。
// data.instructions: {global: bool, project: bool, extra: [path...]}(缺省两者都开)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	cfg := defaultInstrCfg()
	if m != nil && m.Data != nil {
		if raw, ok := m.Data["instructions"].(map[string]any); ok {
			if g, ok := raw["global"].(bool); ok {
				cfg.global = g
			}
			if pr, ok := raw["project"].(bool); ok {
				cfg.project = pr
			}
			if xs, ok := raw["extra"].([]any); ok {
				for _, x := range xs {
					if s, ok := x.(string); ok {
						cfg.extra = append(cfg.extra, s)
					}
				}
			}
		}
	}
	s := &Service{}
	if cfg.global {
		if raw, err := os.ReadFile(globalInstructionsPath()); err == nil {
			s.globalInstr = string(raw)
		}
	}
	if cfg.project {
		if raw, err := os.ReadFile(projectInstructionsPath()); err == nil {
			s.projectInstr = string(raw)
		}
	}
	for _, pth := range cfg.extra {
		if raw, err := os.ReadFile(pth); err == nil {
			s.extraInstr = append(s.extraInstr, string(raw))
		}
	}
	if err := c.Provide("ctx.systemPrompt", s); err != nil {
		return nil, err
	}
	return func() {}, nil
}

type instrCfg struct {
	global  bool
	project bool
	extra   []string
}

func defaultInstrCfg() instrCfg { return instrCfg{global: true, project: true} }

// globalInstructionsPath $GAH_HOME/AGENTS.md(缺省 ~/.gah/AGENTS.md)。
func globalInstructionsPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		uh, err := os.UserHomeDir()
		if err == nil {
			home = filepath.Join(uh, ".gah")
		} else {
			return ""
		}
	}
	return filepath.Join(home, "AGENTS.md")
}

// projectInstructionsPath <cwd>/AGENTS.md(workspace 根 = 启动目录,与沙箱根一致)。
func projectInstructionsPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(wd, "AGENTS.md")
}

// Service 实现 sdk.SystemPromptService。
type Service struct {
	mu           sync.RWMutex
	sections     []sdk.SystemPromptSection
	globalInstr  string
	projectInstr string
	extraInstr   []string
}

// AddSection 注册系统提示片段。
func (s *Service) AddSection(sec sdk.SystemPromptSection) sdk.Disposer {
	s.mu.Lock()
	s.sections = append(s.sections, sec)
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, x := range s.sections {
				if x.Name == sec.Name {
					s.sections = append(s.sections[:i], s.sections[i+1:]...)
					return
				}
			}
		})
	}
}

// Assemble 组装消息:引导 → 全局指令 → 项目指令 → 附加 → 片段 → 工具 schema。
func (s *Service) Assemble(history []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString("你是 gah(Go Agent Harness)中的编程代理。遵循用户的指令完成任务。")
	sb.WriteString("\n\n规则:\n- 需要外部信息或操作时,调用可用工具,不要猜测。\n- 工具调用必须通过 API 的结构化 tool_calls 字段发起;禁止在回复正文中书写工具调用标签/标记(如 <tool_calls>、<invoke>、<antml:invoke> 等)——正文中的调用不会被 gah 执行。\n- 工具结果以 JSON 呈现,仅依赖结果内容,不臆造。\n- 若工具返回错误,分析错误后调整策略重试,或明确告知无法完成。\n- 若没有可用工具能完成任务,直接如实说明;不得假装已调用工具或编造调用结果。")
	s.writeInstrBlock(&sb, s.globalInstr, "\n\n全局指令(AGENTS.md,用户级):\n")
	s.writeInstrBlock(&sb, s.projectInstr, "\n\n项目指令(AGENTS.md,项目级):\n")
	for i, e := range s.extraInstr {
		s.writeInstrBlock(&sb, e, fmt.Sprintf("\n\n附加指令 %d:\n", i+1))
	}
	for _, sec := range s.sections {
		sb.WriteString("\n\n")
		sb.WriteString(sec.Name)
		sb.WriteString(":\n")
		sb.WriteString(sec.Content())
	}
	if len(tools) > 0 {
		sb.WriteString("\n\n可用工具(schema 为 JSON Schema):\n")
		for _, t := range tools {
			schema, _ := json.Marshal(t.InputSchema)
			sb.WriteString(fmt.Sprintf("- %s: %s\n  inputSchema: %s\n", t.Name, t.Description, schema))
		}
	}
	system := sdk.LLMMessage{Role: sdk.RoleSystem, Content: sb.String()}
	return append([]sdk.LLMMessage{system}, history...)
}

func (s *Service) writeInstrBlock(sb *strings.Builder, content, header string) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return
	}
	sb.WriteString(header)
	sb.WriteString(trimmed)
}
