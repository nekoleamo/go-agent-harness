// Package hostmemory:跨会话记忆的宿主侧(注入 + 服务),对应 internal/memory 的落盘。
//
// 做什么:
//   - 注册系统提示片段(`AddSection` + `sdk.SlotDefault`)= 排在**指令层之后**,
//     所以记忆**不能覆盖**角色工作规则与 AGENTS.md;片段正文带围栏与"以指令为准"声明,
//     把记忆按**数据**处理(与 web_fetch 工具结果同一纪律,防提示注入)。
//   - Provide `ctx.memory`,供命令与面板经宿主读改。
//
// 不做什么(边界,写在代码里免得后来以为漏了):
//   - **不自动提取记忆**。自动提取意味着"模型自己决定把什么写进长期记忆",那是静默提权
//     (记忆会长期影响后续每次对话)。M1 只做手动(`/memory add`);自动提取是 M2,须先
//     把「候选 + 用户批量确认 + 限流」三件事做完。
//   - **不静默失败**:记忆文件读不出来/预算截断,都不假装"没有记忆";截断会写在片段里。
package hostmemory

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-memory。
type Plugin struct{}

// Name 插件 id。
func (p *Plugin) Name() string { return "host-memory" }

// Start 注册记忆服务与系统提示片段。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	budget := memory.DefaultInjectBudget
	if m != nil && m.Data != nil {
		if v, ok := m.Data["budget_bytes"].(int); ok && v > 0 {
			budget = v
		}
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		return nil, err // 缺系统提示服务 = 记忆无处可注入,显式失败(不静默只落盘)
	}
	svc := &Service{c: c, budget: budget}
	// 片段内容每次组装现算(记忆随时可改,不能靠 Disposer 撤销重加来"刷新")。
	d := sp.AddSection(sdk.SystemPromptSection{
		Name:    "跨会话记忆",
		Content: func() string { return svc.section() },
		Slot:    sdk.SlotDefault,
	})
	if err := c.Provide("ctx.memory", sdk.MemoryService(svc)); err != nil {
		d()
		return nil, err
	}
	return func() { d() }, nil
}

// Service 实现 sdk.MemoryService。
type Service struct {
	c      sdk.Ctx
	budget int
}

// userEntries / projectEntries 读两级记忆;项目级用**当前打开会话的 projectKey**。
func (s *Service) projectEntries() []memory.Entry {
	var cs sdk.CwdSessions
	if err := s.c.Inject("ctx.cwdSessions", &cs); err != nil || cs == nil {
		return nil
	}
	es, err := memory.Read(memory.ProjectPath(cs.Current()))
	if err != nil {
		s.c.Logger().Warn("host-memory: 项目记忆读取失败(本轮不注入项目级)", "err", err)
		return nil
	}
	return es
}

func (s *Service) userEntries() []memory.Entry {
	es, err := memory.Read(memory.UserPath())
	if err != nil {
		s.c.Logger().Warn("host-memory: 用户记忆读取失败(本轮不注入用户级)", "err", err)
		return nil
	}
	return es
}

// Enabled 记忆注入是否开启(偏好位:一键关闭后不再注入)。
func (s *Service) Enabled() bool { return !prefs.Load().MemoryOff }

// SetEnabled 开关(返回新状态)。
func (s *Service) SetEnabled(on bool) bool {
	prefs.Update(func(p *prefs.Prefs) { p.MemoryOff = !on })
	return on
}

// Budget 注入预算(字节)。
func (s *Service) Budget() int { return s.budget }

// Add 手工加一条记忆到用户级(source 可空 = 手工写,无来源可追溯)。
func (s *Service) Add(content, source string) error {
	return memory.Append(memory.UserPath(), content, source)
}

// List 用户级记忆的展示行(新的在前;带序号与来源标注)。
func (s *Service) List() []string {
	return displayLines(memory.SortedForDisplay(s.userEntries()))
}

// ListProject 项目级记忆的展示行(新的在前)。
func (s *Service) ListProject() []string {
	return displayLines(memory.SortedForDisplay(s.projectEntries()))
}

// displayLines 渲染展示行:序号 + 日期 + 内容 + 来源。
func displayLines(es []memory.Entry) []string {
	out := make([]string, 0, len(es))
	for i, e := range es {
		line := fmt.Sprintf("%d. [%s] %s", i+1, e.Date, e.Content)
		if e.Source != "" {
			line += " (来源: 会话 " + e.Source + ")"
		}
		out = append(out, line)
	}
	return out
}

// Remove 按 List 的序号删一条(序号是"新的在前",内部换成文件序索引)。
func (s *Service) Remove(index int) (string, error) {
	path := memory.UserPath()
	es, err := memory.Read(path)
	if err != nil {
		return "", err
	}
	if index < 1 || index > len(es) {
		return "", fmt.Errorf("序号 %d 越界(共 %d 条;用 /memory list 看编号)", index, len(es))
	}
	e, err := memory.Remove(path, len(es)-index+1)
	if err != nil {
		return "", err
	}
	return "已删除:" + e.Content, nil
}

// RemoveBySource 按来源会话删用户级记忆(治理关键动作)。
func (s *Service) RemoveBySource(source string) (int, error) {
	return memory.RemoveBySource(memory.UserPath(), source)
}

// section 组装注入片段;关掉时返回空串(片段自动不渲染内容)。
func (s *Service) section() string {
	if !s.Enabled() {
		return ""
	}
	user := s.userEntries()
	project := s.projectEntries()
	if len(user) == 0 && len(project) == 0 {
		return ""
	}
	text := memory.Inject(user, project, s.budget)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	// 诚实标注:预算截断发生时告诉用户"还有更多,但没进上下文"。
	total := len(user) + len(project)
	injected := countLines(text)
	if injected < total {
		text += fmt.Sprintf("\n(共 %d 条记忆,预算内放进了 %d 条;其余未进上下文——用 `/memory list` 看全部)\n", total, injected)
	}
	return text
}

// countLines 数注入块里的条目行(以 "  · " 开头)。
func countLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "  · ") {
			n++
		}
	}
	return n
}

var _ sdk.MemoryService = (*Service)(nil)
