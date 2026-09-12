// Package hostsystemprompt 提供 host-system-prompt 插件:ctx.systemPrompt 服务。
// 组装模型可见消息:固定引导 + 全局/项目指令(AGENTS.md)+ 注册片段 + 工具名清单 + 历史。
// **工具 schema 不写入提示词**:完整定义(schema)已由 agent-loop 经 `LLMRequest.Tools` 结构化下发,
// 再以文本重复一份会让同一份 schema 计费两次(MCP 工具尤其昂贵)。此处只留一行纯名称清单
// (约 20 工具 ~100 token),供模型快速总览"有什么工具",不再携带 description/schema 正文。
// 指令注入对齐 pi 语义:全局 $GAH_HOME/AGENTS.md → 项目 <workspace>/AGENTS.md(后者优先,顺序即覆盖)。
package hostsystemprompt

import (
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
	s := &Service{cfg: cfg}
	s.loadLocked() // 启动读取(含多级上下文 P4-5)
	if err := c.Provide("ctx.systemPrompt", s); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// loadLocked 按配置重读指令文件(启动与 /reload 共用;调用方持有或不持锁——Start 未发布无竞争)。
func (s *Service) loadLocked() {
	if s.cfg.global {
		if raw, err := os.ReadFile(globalInstructionsPath()); err == nil {
			s.globalInstr = string(raw)
		}
	}
	if s.cfg.project {
		// 多级上下文(P4-5):从 cwd 逐级向上收集 AGENTS.md,近者覆盖远者;
		// 同级 AGENTS.override.md 存在时替换该级 AGENTS.md。
		if wd, err := os.Getwd(); err == nil {
			s.projectLevels = projectLevelsWalk(wd)
		}
	}
	for _, pth := range s.cfg.extra {
		if raw, err := os.ReadFile(pth); err == nil {
			s.extraInstr = append(s.extraInstr, string(raw))
		}
	}
}

// ReloadInstructions 实现 sdk.ReloadableInstructions(/reload):按配置重读指令文件;
// 缺失文件按无处理(NotFound = 清除旧值);其它读取错误保留旧值并返回(错误回滚)。
func (s *Service) ReloadInstructions() error {
	gi := s.globalInstr
	lv := s.projectLevels
	var ex []string // 附加指令按 cfg 重建(旧值仅在错误时回滚保留;staticcheck SA4006 修)
	if s.cfg.global {
		raw, err := os.ReadFile(globalInstructionsPath())
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("重载全局指令失败(旧值保留): %w", err)
		}
		if err == nil {
			gi = string(raw)
		} else {
			gi = ""
		}
	}
	if s.cfg.project {
		wd, err := os.Getwd()
		if err == nil {
			lv = projectLevelsWalk(wd)
		}
	}
	for _, p := range s.cfg.extra {
		raw, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("重载附加指令失败(旧值保留): %w", err)
		}
		if err == nil {
			ex = append(ex, string(raw))
		}
	}
	s.mu.Lock()
	s.globalInstr, s.projectLevels, s.extraInstr = gi, lv, ex
	s.mu.Unlock()
	return nil
}

type instrCfg struct {
	global  bool
	project bool
	extra   []string
}

func defaultInstrCfg() instrCfg { return instrCfg{global: true, project: true} }

// globalInstructionsPath $GAH_HOME/AGENTS.md(空仅嵌入/单测 → TempDir,~/.gah 兜底已弃用)。
func globalInstructionsPath() string { return filepath.Join(sdk.Home(), "AGENTS.md") }

// projectLevel 一个层级目录的指令来源(近者覆盖远者;override 同级替换)。
type projectLevel struct {
	dir     string // 所在目录(展示用)
	file    string // 实际文件名(AGENTS.md 或 AGENTS.override.md)
	content string
}

// projectLevelsWalk 从 wd 逐级向上收集指令文件(根 → cwd,近者在后):
// 每级优先取 AGENTS.override.md(存在则忽略同目录 AGENTS.md——override 语义)。
func projectLevelsWalk(wd string) []projectLevel {
	var rev []projectLevel
	dir := wd
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "AGENTS.override.md")); err == nil {
			rev = append(rev, projectLevel{dir: dir, file: "AGENTS.override.md", content: string(b)})
		} else if b, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")); err == nil {
			rev = append(rev, projectLevel{dir: dir, file: "AGENTS.md", content: string(b)})
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// 反转为远→近(近者在后,覆盖语义)
	out := make([]projectLevel, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}

// Service 实现 sdk.SystemPromptService。
type Service struct {
	mu            sync.RWMutex
	cfg           instrCfg // 启动配置(/reload 重读依据)
	sections      []sdk.SystemPromptSection
	globalInstr   string
	projectInstr  string // 单级回退(测试构造/旧路径);多级经 projectLevels
	projectLevels []projectLevel
	extraInstr    []string
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

// Assemble 组装消息:引导 → 全局指令 → 项目指令 → 附加 → 片段 → 工具名清单。
// tools 只用于生成名称清单;完整定义由调用方经 LLMRequest.Tools 结构化下发(见包注释)。
func (s *Service) Assemble(history []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString("你是 gah(Go Agent Harness)中的编程代理。遵循用户的指令完成任务。")
	sb.WriteString("\n\n规则:\n- 需要外部信息或操作时,调用可用工具,不要猜测。\n- 工具调用必须通过 API 的结构化 tool_calls 字段发起;禁止在回复正文中书写工具调用标签/标记(如 <tool_calls>、<invoke>、<antml:invoke> 等)——正文中的调用不会被 gah 执行。\n- 工具结果以 JSON 呈现,仅依赖结果内容,不臆造。\n- 若工具返回错误,分析错误后调整策略重试,或明确告知无法完成。\n- 若没有可用工具能完成任务,直接如实说明;不得假装已调用工具或编造调用结果。")
	s.writeInstrBlock(&sb, s.globalInstr, "\n\n全局指令(AGENTS.md,用户级):\n")
	if len(s.projectLevels) > 0 {
		// 多级(P4-5):根 → cwd 逐级注入,近者放后覆盖远者;每级标明来源目录
		sb.WriteString("\n\n项目指令(AGENTS.md 层级,从根目录到当前目录,近者覆盖远者):\n")
		for _, lv := range s.projectLevels {
			s.writeInstrBlock(&sb, lv.content, fmt.Sprintf("- 来自 %s/%s:\n", lv.dir, lv.file))
		}
	} else {
		s.writeInstrBlock(&sb, s.projectInstr, "\n\n项目指令(AGENTS.md,项目级):\n")
	}
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
		// 仅名称,不带 description/schema(结构化下发已含全文;防双重计费,见包注释)。
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
		}
		sb.WriteString("\n\n可用工具:")
		sb.WriteString(strings.Join(names, "、"))
		sb.WriteString("(完整定义与参数见 API 的 tools 字段)")
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
