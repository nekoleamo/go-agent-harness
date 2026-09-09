// host-confirm-fusion:审批确认融合仲裁(P3 三端融合)。Web/TUI/IM 同进程并存时
// ctx.confirm 提供方冲突(core/ctx 同名拒绝)——本插件统一 Provide ctx.confirm(Fusion
// 实现),各 UI 插件改为经 ctx.confirmFusion.Register 注册呈现者(web 弹层/im 文字
// y·n/tui 弹层),任一渠道应答即生效(双端同卡同决策)。
// 装配了 Fusion 的 profile(base+web+im-qq+confirm-fusion)不再互斥;未装配 Fusion 的
// 既有 profile(web-only/tui-only/im-only)保持各 UI 自 Provide ctx.confirm(向后兼容)。
package hostconfirmfusion

import (
	"context"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Fusion ctx.confirm 实现 + 呈现者注册表。并发安全。
type Fusion struct {
	mu          sync.Mutex
	presenters  map[string]sdk.ConfirmPresenter // 注册顺序无关;应答竞速
	registerSeq int
}

// Plugin 实现 host-confirm-fusion。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-confirm-fusion" }

// Start 提供 ctx.confirm(Fusion)+ ctx.confirmFusion。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	if err := c.Provide("ctx.confirm", f); err != nil {
		return nil, err
	}
	if err := c.Provide("ctx.confirmFusion", f); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Register 注册渠道呈现者(Disposer 幂等撤销)。
func (f *Fusion) Register(channel string, p sdk.ConfirmPresenter) sdk.Disposer {
	f.mu.Lock()
	f.registerSeq++
	key := channel
	if _, dup := f.presenters[key]; dup {
		// 同名渠道后注册覆盖(热重载/重复装配安全)
	}
	f.presenters[key] = p
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		if f.presenters[key] == p {
			delete(f.presenters, key)
		}
		f.mu.Unlock()
	}
}

// Channels 已注册渠道(诊断/状态)。
func (f *Fusion) Channels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.presenters))
	for k := range f.presenters {
		out = append(out, k)
	}
	return out
}

// Confirm sdk.ConfirmService:广播呈现给所有渠道,presenter 竞速应答,首个生效。
// 无已注册渠道显式报错(策略侧应安全拒绝,不静默放行);ctx 取消按拒绝处理。
func (f *Fusion) Confirm(ctx context.Context, prompt string) (bool, error) {
	f.mu.Lock()
	presenters := make([]sdk.ConfirmPresenter, 0, len(f.presenters))
	for _, p := range f.presenters {
		presenters = append(presenters, p)
	}
	f.mu.Unlock()
	if len(presenters) == 0 {
		return false, context.Canceled // 无确认通道:按取消(拒绝)处理
	}
	// 每渠道一次 Present(prompt);失败渠道跳过(其 UI 不可用)
	type active struct {
		ch     <-chan bool
		cancel func()
	}
	var cases []active
	for _, p := range presenters {
		ch, cancel, err := p.Present(ctx, prompt)
		if err != nil || ch == nil {
			continue
		}
		cases = append(cases, active{ch: ch, cancel: cancel})
	}
	defer func() { // 结束路径统一清理各渠道呈现(幂等)
		for _, c := range cases {
			c.cancel()
		}
	}()
	if len(cases) == 0 {
		return false, context.Canceled
	}
	// 竞速:任一渠道应答即返回(先答先得;多于一个渠道合并监听)
	if len(cases) == 1 {
		select {
		case ok := <-cases[0].ch:
			return ok, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	merged := make(chan bool, 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(cases))
	for _, c := range cases {
		go func(ac active) {
			defer wg.Done()
			select {
			case ok := <-ac.ch:
				select {
				case merged <- ok:
				default:
				}
			case <-done:
			}
		}(c)
	}
	go func() { wg.Wait(); close(merged) }()
	defer close(done)
	select {
	case ok := <-merged:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
