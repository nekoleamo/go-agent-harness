// Package acpclient 插件装配:解析 data.runners 并注册 acp_run 工具。
//
// 配置(config/bundle-base.yaml 的 acp-client 条目;缺省**停用** —— 它会拉起一个
// 有完整文件读写能力的编码 agent,默认开着等于把一段"任意本地执行"塞进工具表):
//
//	enabled: true
//	data:
//	  runners:
//	    opencode:    { command: opencode,  args: ["acp"] }
//	    codex:       { command: npx,       args: ["-y", "@zed-industries/codex-acp"] }
//	    claude_code: { command: npx,       args: ["-y", "@zed-industries/claude-agent-acp"] }
//	  timeout_seconds: 600   # 单次调用默认超时(上限 3600)
//
// 一个 runner 都没配 ⇒ **不注册工具**(模型看不到 acp_run)。这与 mcp-bridge 同口径:
// 没有可用的外部程序时,把工具摆进工具表只会换来一串"未配置"的失败调用。
package acpclient

import (
	"fmt"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 acp-client。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "acp-client" }

// Start 解析配置并注册工具(无 runner 时只记一行日志,不注册)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	runners, timeout, err := parseConfig(m)
	if err != nil {
		return nil, err
	}
	if len(runners) == 0 {
		c.Logger().Info("acp-client: 未配置 runners,不注册 acp_run(见 docs/PLUGIN_DEV.md「外部编码 agent 委派」)")
		return func() {}, nil
	}
	var reg sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &reg); err != nil {
		return nil, err
	}
	t := &Tool{c: c, runners: runners, timeout: timeout}
	return reg.Register(t), nil
}

// defaultTimeout 单次委派的默认超时。10 分钟:够跑一轮"改代码 + 跑测试",
// 又不至于一个坏掉的外部 agent 把回合挂到用户去按停止。
const defaultTimeout = 10 * time.Minute

// parseConfig 读 manifest data。类型/字段写错**显式报错** —— 一个静默失效的
// runner 配置表现是"工具不存在",而用户会去查 PATH,查一天也查不出来。
func parseConfig(m *sdk.Manifest) (map[string]runner, time.Duration, error) {
	timeout := defaultTimeout
	if m == nil || m.Data == nil {
		return nil, timeout, nil
	}
	if v, ok := m.Data["timeout_seconds"]; ok {
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return nil, 0, fmt.Errorf("acp-client: data.timeout_seconds 必须是正整数, got %v", v)
		}
		timeout = time.Duration(n) * time.Second
	}
	raw, ok := m.Data["runners"]
	if !ok {
		return nil, timeout, nil
	}
	tbl, ok := raw.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("acp-client: data.runners 必须是「名字 → {command,args}」映射, got %T", raw)
	}
	out := make(map[string]runner, len(tbl))
	for name, v := range tbl {
		entry, ok := v.(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("acp-client: runners.%s 必须是对象, got %T", name, v)
		}
		cmd, _ := entry["command"].(string)
		if cmd == "" {
			return nil, 0, fmt.Errorf("acp-client: runners.%s 缺 command", name)
		}
		var av []string
		switch a := entry["args"].(type) {
		case nil:
		case []any:
			for _, it := range a {
				s, ok := it.(string)
				if !ok {
					return nil, 0, fmt.Errorf("acp-client: runners.%s.args 含非字符串项 %T", name, it)
				}
				av = append(av, s)
			}
		case []string:
			av = a
		default:
			return nil, 0, fmt.Errorf("acp-client: runners.%s.args 必须是字符串列表, got %T", name, a)
		}
		out[name] = runner{command: cmd, args: av}
	}
	return out, timeout, nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), float64(int(n)) == n
	}
	return 0, false
}
