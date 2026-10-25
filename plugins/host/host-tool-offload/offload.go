// Package hosttooloffload 提供 host-tool-offload 插件:工具结果的**数据面/控制面分流**。
//
// 干什么:订阅 tools/post-execute(waterfall),结果超过单条预算时把原文搬进
// sdk.ToolResult.Data,Content 换成「头 + 省略说明 + 尾」(尽量保持 JSON 合法)。
// 模型只吃瘦身版,界面与会话记录留完整原文(Web 的「展开全文」读 Data)。
//
// 为什么是插件而不是 core 里的一个 if:分流改变的是**模型看到什么** —— 那是策略,
// 得能关、能调预算、能按工具豁免(见 data.skip_tools)。关掉即回到"整份进上下文"的
// 旧行为,不需要改一行代码。
//
// 配置(config/bundle-base.yaml 的 data):
//
//	limit: 20000        # 单条工具结果预算(字符);0 = 关闭分流;正数须 >= minLimit
//	skip_tools: [..]    # 豁免工具名(它们自带更精确的预算,例如 file_read 的行区间)
package hosttooloffload

import (
	"context"
	"fmt"

	"github.com/nekoleamo/go-agent-harness/internal/tooloffload"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// defaultLimit 单条工具结果预算(字符)。20000 ≈ 5k token。
//
// 为什么是它:真实会话里常见的"大结果"是构建日志与网页正文(10k~1MB),而正常的
// 工具结果(读几十行、grep 十几条)都在 2k 以下。取 2 万 = 只碰真正会撑爆上下文的
// 那批,不改变绝大多数回合的模型输入。
const defaultLimit = 20000

// minLimit 单条预算下限(字符)。低于它就不再是「给模型瘦身」,而是「把结果切成碎片」:
//
// internal/tooloffload 的 envelope 注释已经写明这个失败模式(模型看到半截 JSON 会去猜它、
// 甚至照着补全),而在这条预算下模型连「这是什么」都读不到 —— 于是它猜出来的东西会被
// 当成事实继续用。上游 Harness² 对同类参数的处理是硬下限(观测截断 < 10k 直接拒绝,
// 理由写的是 "fabricates defects"):同一个坑,同一道闸。
//
// 取 4000(≈1k token):此时说明文字与 JSON 骨架约占 4%,头尾各能分到 1.5k 以上,
// trimJSON 的字段预算(limit*7/10 = 2800)远高于它自己的 256 放弃线。
const minLimit = 4000

// Plugin 实现 host-tool-offload。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "host-tool-offload" }

// config 分流配置(从 manifest data 解析;缺省即默认值)。
type config struct {
	limit int
	skip  map[string]bool
}

// Start 安装 post-execute 分流器。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	cfg, err := parseConfig(m)
	if err != nil {
		return nil, err
	}
	if cfg.limit <= 0 {
		// 显式关掉:不订阅(而不是订阅后什么都不做 —— 少一条每工具都跑的空回调)。
		return func() {}, nil
	}
	return c.Subscribe("tools/post-execute", func(ctx context.Context, ev *sdk.Event) error {
		res, ok := ev.Payload.(*sdk.ToolResult)
		if !ok || res == nil {
			return nil // 别的形态的载荷:不是我们的面(不猜、不改)
		}
		if res.Data != "" {
			return nil // 工具自己已经分好流:尊重它的(它更懂自己的数据)
		}
		if cfg.skip[sdk.ToolNameFrom(ctx)] {
			return nil
		}
		r := tooloffload.Split(res.Content, cfg.limit)
		if !r.Trimmed {
			return nil
		}
		res.Data = r.Data
		res.Content = r.Visible
		return nil
	}), nil
}

// parseConfig 读 manifest data。类型写错**显式报错**:预算写错会让分流彻底不生效,
// 而"分流没生效"在界面上看不出来(只会觉得上下文又炸了)。
func parseConfig(m *sdk.Manifest) (config, error) {
	cfg := config{limit: defaultLimit, skip: map[string]bool{}}
	if m == nil || m.Data == nil {
		return cfg, nil
	}
	if v, ok := m.Data["limit"]; ok {
		n, ok := toInt(v)
		if !ok {
			return cfg, fmt.Errorf("host-tool-offload: data.limit 必须是整数(0 = 关闭), got %T", v)
		}
		cfg.limit = n
	}
	// 显式失败而不是静默抬到下限:静默改用户设的数同样属于「静默改意图」,
	// 而 0 已经是「关闭」的合法写法 —— 报错文案把两条合法选择直接写出来。
	if cfg.limit > 0 && cfg.limit < minLimit {
		return cfg, fmt.Errorf(
			"host-tool-offload: data.limit=%d 太小,会把工具结果切成碎片(模型会去猜被省略的部分);"+
				"改成 0(关闭分流)或 >= %d", cfg.limit, minLimit)
	}
	if v, ok := m.Data["skip_tools"]; ok {
		list, ok := v.([]any)
		if !ok {
			return cfg, fmt.Errorf("host-tool-offload: data.skip_tools 必须是字符串列表, got %T", v)
		}
		for _, it := range list {
			name, ok := it.(string)
			if !ok {
				return cfg, fmt.Errorf("host-tool-offload: data.skip_tools 含非字符串项 %T", it)
			}
			cfg.skip[name] = true
		}
	}
	return cfg, nil
}

// toInt YAML/JSON 数字在配置层可能是 int / float64 / int64。
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
