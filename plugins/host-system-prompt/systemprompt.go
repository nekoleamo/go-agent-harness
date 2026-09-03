// Package hostsystemprompt 提供 host-system-prompt 插件:ctx.systemPrompt 服务。
// 组装模型可见消息:固定引导 + 注册片段 + 工具 schema 清单 + 历史。
package hostsystemprompt

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-system-prompt。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-system-prompt" }

// Start 注册 ctx.systemPrompt 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	s := &Service{}
	if err := c.Provide("ctx.systemPrompt", s); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Service 实现 sdk.SystemPromptService。
type Service struct {
	mu       sync.RWMutex
	sections []sdk.SystemPromptSection
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

// Assemble 组装消息。
func (s *Service) Assemble(history []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString("你是 gah(Go Agent Harness)中的编程代理。遵循用户的指令完成任务。")
	sb.WriteString("\n\n规则:\n- 需要外部信息或操作时,调用可用工具,不要猜测。\n- 工具结果以 JSON 呈现,仅依赖结果内容,不臆造。\n- 若工具返回错误,分析错误后调整策略重试,或明确告知无法完成。")
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
