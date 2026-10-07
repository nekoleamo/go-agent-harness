// Package hostllm 提供 host-llm 插件:ctx.llm 服务(适配器注册 + 路由)。
// 域模型与适配器 seam 定义在 sdk;本插件仅做注册表与默认路由。
package hostllm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-llm。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-llm" }

// Start 注册 ctx.llm 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	s := &Service{adapters: make(map[string]sdk.LLMAdapter), c: c}
	// 会话服务(可选):只用于给 provider 自定义头里的 ${session} 取一个**稳定**的会话 id
	// (OpenCode Go 这类网关按会话优化路由与缓存,官方要求每会话一个稳定值)。
	// 缺它不影响别的功能 —— 拿不到时 ${session} 展开成空串(见 headerVars)。
	var cs sdk.CwdSessions
	_ = c.Inject("ctx.cwdSessions", &cs)
	s.cs = cs
	// 提示通道(可选):会话级模型不可用时如实告警一次(未装配 = 静默回落到全局模型)。
	var notices sdk.NoticeService
	_ = c.Inject("ctx.notices", &notices)
	s.notices = notices
	if err := c.Provide("ctx.llm", s); err != nil {
		return nil, err
	}
	// 会话级模型/思考(第一百一十六批):在请求发出前按**该会话**显式设置过的那份填进去。
	//
	// 为什么挂在这里而不是让 agent-loop 填:模型选择归 LLM 服务管(它才知道这个 provider
	// 有哪些模型、不可用时怎么回落),agent-loop 不该知道模型可用性。
	//
	// 为什么与角色的覆盖**顺序无关**:两边都是"空才填"(req.Model == "" / !req.ThinkingSet),
	// 而扩展点的契约明确要求监听器"只做填空类改写" ⇒ 谁先谁后都不会互相盖掉,
	// 会话级永远赢(它先填,角色发现非空就跳过)。
	dPre := c.Subscribe(sdk.EventLLMPreRequest, s.onPreRequest)
	return func() { dPre() }, nil
}

// onPreRequest 会话级模型/思考档注入。
func (s *Service) onPreRequest(ctx context.Context, ev *sdk.Event) error {
	req, ok := ev.Payload.(*sdk.LLMRequest)
	if !ok || req == nil {
		return nil
	}
	prefs := s.sessionPrefs(ctx)
	if prefs.Model != "" && req.Model == "" {
		if s.ModelUsable(prefs.Model) {
			req.Model = prefs.Model
		} else {
			// 不可用:如实告警一次并回落全局 —— 让整轮请求因为一个拼错的名字失败更糟。
			s.warnModelUnusable(prefs.Model)
		}
	}
	if prefs.Thinking != "" && !req.ThinkingSet {
		req.Thinking = sdk.ParseThinking(prefs.Thinking)
		req.ThinkingSet = true // 显式:含 off,否则会被会话级 thinking 回填(同角色那条的教训)
	}
	return nil
}

// sessionPrefs 该次请求所属会话的偏好(拿不到会话服务/会话没设 = 零值 ⇒ 交给全局兜底)。
func (s *Service) sessionPrefs(ctx context.Context) sdk.SessionPrefs {
	src, ok := s.cs.(sdk.SessionPrefsSource)
	if !ok || src == nil {
		return sdk.SessionPrefs{}
	}
	return src.SessionPrefsOf(sdk.SessionFromContext(ctx))
}

// ModelUsable 实现 sdk.ModelAvailability:本地判断模型名是否可用(不发请求)。
//
// 与 host-roles 里那份私有判定同源(都走 sdk.ModelCatalog);抽成接口是因为
// 两处都要问 —— 角色声明的模型与会话级模型。判不了(未实现 ModelCatalog)一律当可用。
func (s *Service) ModelUsable(model string) bool {
	if model == "" {
		return true
	}
	cat := s.catalog
	if cat == nil {
		cat, _ = s.c.(sdk.ModelCatalog)
	}
	if cat == nil {
		return true // 本地判不了:不当成不可用(否则会把能跑的模型也拒了)
	}
	return cat.KnownModel(model)
}

// warnModelUnusable 会话级模型不可用时告警一次(不阻断:回落到全局模型继续跑)。
func (s *Service) warnModelUnusable(model string) {
	if s.notices == nil {
		return
	}
	s.notices.Publish(sdk.Notice{
		Level:  "warn",
		Title:  "本会话的模型不可用",
		Body:   "已改用当前模型继续:" + model,
		Source: "host-llm",
	})
}

// Service 实现 sdk.LLMService。
type Service struct {
	// c 宿主 ctx:只为发 `llm/pre-request` 扩展点事件(第八十六批)。为何存这里而不是让调用方发:
	// 本服务的 Complete 是**所有模型请求的唯一必经点**(主回合 agent-loop、并行子代理 host-fanout、
	// 汇总 host-session-summary,以后的调用方同样走它) —— 把事件发在这里,一次就覆盖全部,
	// 且各调用方零改动。代价:本服务从"纯注册表"变成"也发事件",故注释写明。
	// 单测里可能为 nil(大量既有单测直接构造 &Service{}),故使用处必须 nil 守卫。
	c sdk.Ctx

	mu       sync.RWMutex
	adapters map[string]sdk.LLMAdapter
	order    []string // 注册顺序(首个为默认候选)
	model    string
	thinking sdk.ThinkingLevel // 会话级思考等级(Tab 循环;默认 Off)

	// cs 会话服务(可选,nil 兼容单测的裸 &Service{}):只喂 provider 自定义头的 ${session}。
	cs              sdk.CwdSessions
	notices         sdk.NoticeService       // 可选(ctx.notices 未装配 = nil;会话级模型不可用时告警)
	catalog         sdk.ModelCatalog        // 可选(模型可用性判定;缺 = 一律当可用)。字段仅为可测
	provModelsCache []sdk.ProviderModelList // ListAllModels TTL 缓存
	provModelsAt    time.Time               // 缓存写入时刻
}

// RegisterAdapter 注册适配器。
func (s *Service) RegisterAdapter(a sdk.LLMAdapter) sdk.Disposer {
	s.mu.Lock()
	if _, ok := s.adapters[a.Name()]; ok {
		s.mu.Unlock()
		return func() {} // 重名不致命:返回 no-op
	}
	s.adapters[a.Name()] = a
	s.order = append(s.order, a.Name())
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.adapters, a.Name())
			for i, n := range s.order {
				if n == a.Name() {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
		})
	}
}

// completeAdapter 路由当前模型到适配器:模型名精确/前缀命中 ModelRouter 声明优先;
// 无命中 → 首个注册者(默认回退,对齐原语义)。
func (s *Service) completeAdapter(model string) (sdk.LLMAdapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.order) == 0 {
		return nil, fmt.Errorf("llm: 无可用 LLM 适配器(适配器插件 llm-* 已卸载或未启用,回合无法继续)")
	}
	if model != "" {
		for _, a := range s.adapters {
			if r, ok := a.(sdk.ModelRouter); ok {
				for _, m := range r.Models() {
					if model == m || strings.HasPrefix(model, m+"-") {
						return a, nil
					}
				}
			}
		}
	}
	// 默认回退:首个未声明模型前缀的通用适配器(与注册顺序解耦;
	// 全为前缀声明型时才落 order[0])
	for _, n := range s.order {
		a := s.adapters[n]
		if _, ok := a.(sdk.ModelRouter); !ok {
			return a, nil
		}
	}
	return s.adapters[s.order[0]], nil
}

// KnownModel 本地判定「这个模型名有适配器能接」(sdk.ModelCatalog;不发任何网络请求)。
// 判据与 completeAdapter 的路由一致:任一适配器声明的前缀命中即 true。
// 两个"判不了就放行"的分支① 存在**未声明模型前缀的通用适配器**(它接受任意模型名);
// ② 一个适配器都没注册时不报 false(该情况由 Complete 的"还没有配置模型"负责。
func (s *Service) KnownModel(model string) bool {
	if model == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	generic := false
	for _, a := range s.adapters {
		r, ok := a.(sdk.ModelRouter)
		if !ok {
			generic = true
			continue
		}
		for _, m := range r.Models() {
			if model == m || strings.HasPrefix(model, m+"-") {
				return true
			}
		}
	}
	return generic
}

// Complete 以默认适配器发起请求;对可重试错误实施指数退避重试(§11 矩阵:网络断流/5xx 可重试,4xx 不可重试)。
func (s *Service) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	// llm/pre-request:模型请求发出前的 waterfall 扩展点(第八十六批)。
	// 监听器可原地改写 *req(典型:当前角色声明的模型/思考档);返回 error = 阻断本次请求。
	// 三条要点:
	//   - 必须在取 s.mu **之前**发 —— 监听器会去读角色状态(另一把锁),没理由交叉持锁;
	//   - 在"模型解析"之前发 ⇒ 监听器给的模型即便会话未配模型也能跑(不再依赖会话先配好);
	//   - 只在这里发:retry() 直调适配器,所以一个逻辑请求只 emit 一次(监听器仍需幂等,
	//     因为溢出兜底重试会重新进入 Complete —— 那是另一次请求,理应重新经过本扩展点)。
	if s.c != nil {
		if _, err := s.c.Emit(ctx, sdk.EventLLMPreRequest, req, sdk.Waterfall); err != nil {
			return nil, err
		}
	}
	model := req.Model
	if model == "" {
		s.mu.RLock()
		model = s.model
		s.mu.RUnlock()
	}
	if model == "" {
		return nil, fmt.Errorf("llm: 还没有配置模型:先用 /provider 或 Web 的「设置 → Provider」配置一个端点与 API Key")
	}
	// 思考等级注入:调用方**未显式设置**时用会话级(Tab 切换的等级;agent-loop 无需感知)。
	// ThinkingSet 区分"没设置"与"显式 off"—— 否则角色声明的 off 会被当成未设置而被会话档回填。
	if req.Thinking == sdk.ThinkingOff && !req.ThinkingSet {
		s.mu.RLock()
		req.Thinking = s.thinking
		s.mu.RUnlock()
	}
	a, err := s.completeAdapter(model)
	if err != nil {
		return nil, err
	}
	req.Model = model
	return retry(ctx, a, req, onChunk)
}

// retry 指数退避重试:最多 3 次,间隔 1s/2s/4s;仅重试 sdk.RetryableError;ctx 取消立即返回。
func retry(ctx context.Context, a sdk.LLMAdapter, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	const maxAttempts = 3
	var last error
	for i := 0; i < maxAttempts; i++ {
		resp, err := a.Complete(ctx, req, onChunk)
		if err == nil {
			return resp, nil
		}
		var retryable *sdk.RetryableError
		if !errors.As(err, &retryable) || ctx.Err() != nil {
			return nil, err // 不可重试或已取消
		}
		last = err
		if i == maxAttempts-1 {
			break
		}
		backoff := time.Duration(1<<i) * time.Second // 1s, 2s, 4s
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, last
}

// SetModel 设置当前模型名。
func (s *Service) SetModel(model string) {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()
}

// —— 多 provider 并存(sdk.MultiProviderService;provider.yaml 为单一事实源,add/use/set
// 先持久化再由运行时按活跃同步适配器端点与模型;env 显式仍最高——用户显式操作覆盖) ——

// provModelsTTL ListAllModels 缓存时长(与适配器模型缓存同量级)。
const provModelsTTL = 10 * time.Minute

// Providers 全部 provider 运行时视图(文件活跃标记)。
func (s *Service) Providers() []sdk.ProviderProfile {
	f, err := providerfile.LoadFile()
	if err != nil {
		return nil
	}
	out := make([]sdk.ProviderProfile, 0, len(f.Providers))
	for _, p := range f.Providers {
		out = append(out, sdk.ProviderProfile{Name: p.Name, BaseURL: p.BaseURL,
			APIKey: p.APIKey, Model: p.Model, Active: p.Name == f.Active,
			Headers: providerfile.ExpandHeaders(p.Headers, s.headerVars())})
	}
	return out
}

// AddProvider 新增/更新 provider(同名 upsert 并激活;首个自动激活)。
// 成为活跃时立即同步适配器端点与模型(运行时即生效)。
func (s *Service) AddProvider(name, baseURL, apiKey, model string) error {
	if name == "" {
		name = providerfile.ShortNameOf(baseURL)
	}
	if err := providerfile.Add(providerfile.Provider{Name: name, BaseURL: baseURL, APIKey: apiKey, Model: model}); err != nil {
		return err
	}
	// 新增/更新后必须清聚合缓存:否则首启自检(Web 保存后立即拉模型列表)
	// 会在 10 分钟 TTL 内拿到「还没这个 provider」或旧 Key 的过期结果。
	s.invalidateModelsCache()
	f, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	if f.Active != name {
		return nil // 新增非活跃:仅并存,不切运行时
	}
	p, ok := findProvider(f, name)
	if !ok {
		return nil
	}
	return s.switchActive(p)
}

// SetActiveProvider 切换活跃 provider(校验存在;立即同步适配器端点与模型并持久化 active)。
func (s *Service) SetActiveProvider(name string) error {
	if err := providerfile.SetActive(name); err != nil {
		return err
	}
	f, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	p, ok := findProvider(f, name)
	if !ok {
		return fmt.Errorf("provider: 不存在 %q", name)
	}
	return s.switchActive(p)
}

// RemoveProvider 删除一个 provider(provider.yaml 为单一事实源:先落盘再同步运行时)。
// 删非活跃只掉列表;删活跃则活跃顺延到剩余首个并立即 Configure 生效;删空回退 env/样板。
func (s *Service) RemoveProvider(name string) error {
	f, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	if _, ok := findProvider(f, name); !ok {
		return fmt.Errorf("provider: 不存在 %q(/provider show 查看)", name)
	}
	wasActive := f.Active == name
	if err := providerfile.Remove(name); err != nil {
		return err
	}
	// 聚合缓存必须同步失效:否则被删端点在 /model 下拉里滞留到 TTL 到期(删了还在)。
	s.invalidateModelsCache()
	if !wasActive {
		return nil
	}
	next, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	if next.Active == "" {
		return s.ResetProvider() // 删空:回退启动默认(env/样板)
	}
	p, ok := findProvider(next, next.Active)
	if !ok {
		return nil
	}
	return s.switchActive(p)
}

// findProvider 从文件视图按名取 provider。
func findProvider(f providerfile.File, name string) (providerfile.Provider, bool) {
	for _, p := range f.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return providerfile.Provider{}, false
}

// switchActive 同步适配器到活跃 provider 端点/模型(Configure + SetModel;零重启)。
// currentModel 当前模型名(锁内读)。
func (s *Service) currentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// matchedAdapter 按模型前缀找出真正会服务该模型的适配器(同 completeAdapter 的匹配口径)。
func (s *Service) matchedAdapter(model string) sdk.LLMAdapter {
	if model == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.adapters {
		r, ok := a.(sdk.ModelRouter)
		if !ok {
			continue
		}
		for _, m := range r.Models() {
			if model == m || strings.HasPrefix(model, m+"-") {
				return a
			}
		}
	}
	return nil
}

// providerAdapterFor 选「真正会服务该模型」的可配置适配器:
// 模型前缀命中且实现 ProviderAdapter → 用它;未命中 → 通用适配器。
// 命中却不支持运行时配置 → 显式报错(不偷偷配到别的适配器上)。
// 2026-09-21 修:此前一律配通用适配器 → claude 走 anthropic 适配器、base_url 却配在 openai 适配器上
// (运行期切换对 claude 模型静默失效)。
func (s *Service) providerAdapterFor(model string) (sdk.ProviderAdapter, error) {
	if a := s.matchedAdapter(model); a != nil {
		pa, ok := a.(sdk.ProviderAdapter)
		if !ok {
			return nil, fmt.Errorf("llm: 适配器 %s 不支持运行时 provider 配置(未实现 sdk.ProviderAdapter)", a.Name())
		}
		return pa, nil
	}
	return s.genericProvider()
}

func (s *Service) switchActive(p providerfile.Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("provider: %s 未配置 base_url(请 /provider set 补齐)", p.Name)
	}
	pa, err := s.providerAdapterFor(p.Model)
	if err != nil {
		return err
	}
	if err := pa.Configure(p.BaseURL, p.APIKey); err != nil {
		return err
	}
	// 自定义头在 Configure **之后**下发:Configure 会清掉上一端点的头(端点变了,
	// 旧头的语义未必还成立)。适配器没实现 HeaderConfigurable 时静默忽略 —— 那是可选能力
	// (多数适配器不需要自定义头),不该让一个 provider 的配置把启动搞挂。
	if hc, ok := pa.(sdk.HeaderConfigurable); ok {
		hc.ConfigureHeaders(s.headersFor(p))
	}
	if p.Model != "" {
		s.SetModel(p.Model)
	}
	s.invalidateModelsCache() // 端点已切:聚合缓存(含各端点 Err)同步失效
	return nil
}

// invalidateModelsCache 清聚合模型缓存(provider 增删/切换后调用;TTL 缓存不能自己知道这些变更)。
// 调用方不得持有 s.mu(本方法自行加锁)。
func (s *Service) invalidateModelsCache() {
	s.mu.Lock()
	s.provModelsCache = nil
	s.provModelsAt = time.Time{}
	s.mu.Unlock()
}

// ListAllModels 聚合所有 provider 端点 /models(TTL 缓存;单条失败记入其 Err,不整体失败)。
// 活跃 provider 走通用适配器 ListModels(复用其 TTL 缓存),非活跃经 sdk.OpenAIFetchModels 直拉。
func (s *Service) ListAllModels() []sdk.ProviderModelList {
	s.mu.RLock()
	if s.provModelsCache != nil && time.Since(s.provModelsAt) < provModelsTTL {
		out := append([]sdk.ProviderModelList(nil), s.provModelsCache...)
		s.mu.RUnlock()
		return out
	}
	s.mu.RUnlock()

	f, err := providerfile.LoadFile()
	if err != nil {
		return nil
	}
	out := make([]sdk.ProviderModelList, 0, len(f.Providers))
	for _, p := range f.Providers {
		if p.Name == f.Active {
			out = append(out, s.listActiveModels(p))
		} else {
			m, e := sdk.OpenAIFetchModels(p.BaseURL, p.APIKey)
			out = append(out, sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Models: m, Err: e})
		}
	}
	s.mu.Lock()
	s.provModelsCache = out
	s.provModelsAt = time.Now()
	s.mu.Unlock()
	return out
}

// listActiveModels 活跃 provider 的模型列表(经通用适配器 ModelLister,复用其端点缓存)。
func (s *Service) listActiveModels(p providerfile.Provider) sdk.ProviderModelList {
	pa, err := s.genericProvider()
	if err != nil {
		return sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Err: err}
	}
	if ml, ok := pa.(sdk.ModelLister); ok {
		m, e := ml.ListModels()
		return sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Models: m, Err: e}
	}
	m, e := sdk.OpenAIFetchModels(p.BaseURL, p.APIKey)
	return sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Models: m, Err: e}
}

// genericProvider 当前通用适配器(非 ModelRouter 声明型,即 openai 兼容类)。
func (s *Service) genericProvider() (sdk.ProviderAdapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var fallback sdk.LLMAdapter
	for _, n := range s.order {
		a := s.adapters[n]
		if _, ok := a.(sdk.ModelRouter); ok {
			continue
		}
		if pa, ok := a.(sdk.ProviderAdapter); ok {
			return pa, nil
		}
		if fallback == nil {
			fallback = a
		}
	}
	if fallback != nil {
		return nil, fmt.Errorf("llm: 通用适配器 %s 不支持运行时 provider 配置(未实现 sdk.ProviderAdapter)", fallback.Name())
	}
	return nil, fmt.Errorf("llm: 无通用 LLM 适配器(adapter 插件未装配),无法配置 provider")
}

// SetProvider 运行时切换端点与凭据(TUI /provider set;零重启)。
func (s *Service) SetProvider(baseURL, apiKey string) error {
	pa, err := s.providerAdapterFor(s.currentModel())
	if err != nil {
		return err
	}
	return pa.Configure(baseURL, apiKey)
}

// UnsetProvider 逐项删除配置(TUI /provider unset;该项恢复启动默认)。
func (s *Service) UnsetProvider(field string) error {
	pa, err := s.providerAdapterFor(s.currentModel())
	if err != nil {
		return err
	}
	return pa.Unset(field)
}

// ResetProvider 恢复全部字段为启动默认(TUI /provider clear)。
func (s *Service) ResetProvider() error {
	pa, err := s.providerAdapterFor(s.currentModel())
	if err != nil {
		return err
	}
	return pa.Reset()
}

// ListModels 当前通用适配器端点可用模型列表(TUI /model 动态枚举)。
func (s *Service) ListModels() ([]sdk.ModelInfo, error) {
	pa, err := s.providerAdapterFor(s.currentModel())
	if err != nil {
		return nil, err
	}
	if ml, ok := pa.(sdk.ModelLister); ok {
		return ml.ListModels()
	}
	return nil, fmt.Errorf("llm: 通用适配器 %s 不支持模型列举(未实现 sdk.ModelLister)", paName(pa))
}

// SetThinking 设置会话级思考等级(思考等级注入未显式设置的请求)。
func (s *Service) SetThinking(t sdk.ThinkingLevel) {
	s.mu.Lock()
	s.thinking = t
	s.mu.Unlock()
}

// Thinking 当前会话级思考等级。
func (s *Service) Thinking() sdk.ThinkingLevel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thinking
}

// paName ProviderAdapter 的名称(展示用)。
func paName(pa sdk.ProviderAdapter) string {
	if n, ok := pa.(interface{ Name() string }); ok {
		return n.Name()
	}
	return fmt.Sprintf("%T", pa)
}

// ProviderInfo 当前通用适配器的端点与凭据(展示用;key 由调用方打码)。
func (s *Service) ProviderInfo() (string, string, bool) {
	pa, err := s.genericProvider()
	if err != nil {
		return "", "", false
	}
	u, k := pa.ProviderInfo()
	return u, k, true
}

// Model 返回当前模型名。
func (s *Service) Model() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// List 列出已注册适配器。
func (s *Service) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.order...)
}

// headersFor 展开某 provider 的自定义头(占位符在本层替换,适配器只收最终值)。
//
// 为什么占位符在这层而不在适配器:适配器不知道「当前会话」是什么,那是宿主的概念
// (会话由 host-cwd-sessions 管);让适配器自己猜就会各猜各的。
func (s *Service) headersFor(p providerfile.Provider) sdk.ProviderHeaders {
	if len(p.Headers) == 0 {
		return nil
	}
	return providerfile.ExpandHeaders(p.Headers, s.headerVars())
}

// headerVars 占位符取值。会话 id 的口径:有当前会话就用它,主会话(空)回落项目 key ——
// 官方要的语义是「同一段对话稳定、不同对话不同」,项目 key 恰好满足后半句。
func (s *Service) headerVars() providerfile.HeaderVars {
	v := providerfile.HeaderVars{Version: strings.TrimSpace(os.Getenv("GAH_VERSION"))}
	if s.cs != nil {
		v.Session = s.cs.CurrentSession()
		if v.Session == "" {
			v.Session = s.cs.Current()
		}
	}
	if wd, err := os.Getwd(); err == nil {
		v.CWD = wd
	}
	return v
}

// SetProviderHeaders 单独设置某 provider 的自定义请求头(sdk.ProviderHeadersMutable)。
//
// 为什么要独立于 AddProvider:见接口注释。行为上刻意做到「写盘 + 若是活跃则立即下发」,
// 不拆成两步 —— 否则用户改完头要切走再切回来才生效,那是个很容易被当成 bug 的中间态。
func (s *Service) SetProviderHeaders(name string, h map[string]string) error {
	f, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	if _, ok := findProvider(f, name); !ok {
		return fmt.Errorf("provider: %s 不存在", name)
	}
	if err := providerfile.UpdateHeaders(name, h); err != nil {
		return err
	}
	if f.Active != name {
		return nil // 非活跃:只落盘(列表里改一个 provider 不应该顺手改当前在用的那个)
	}
	// **重新加载**:上面那份 f 是写入**前**的快照,里面的 p.Headers 还是旧的 ——
	// 用它下发会把「刚设的头」当成「没有头」(第一次实现就踩了这个,被测试当场逮住)。
	f2, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	p, ok := findProvider(f2, name)
	if !ok {
		return nil
	}
	if pa, err := s.providerAdapterFor(p.Model); err == nil {
		if hc, ok := pa.(sdk.HeaderConfigurable); ok {
			hc.ConfigureHeaders(s.headersFor(p))
		}
	}
	s.invalidateModelsCache()
	return nil
}

var _ sdk.ProviderHeadersMutable = (*Service)(nil)
