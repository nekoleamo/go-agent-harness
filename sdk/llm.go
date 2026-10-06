// LLM 统一域模型与适配器 seam(对齐设计 §5.2:模型层不感知提供商)。
// 适配器实现本包接口,提供商差异在适配器内映射;宿主仅消费域模型。
package sdk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 模型请求调用的工具。Arguments 是 JSON 字符串。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// AttachmentKind 附件类型(image=视觉注入;file=路径引用文本,模型经工具读)。
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
)

// Attachment 消息附件(附件一期):image 经适配器构造结构化 image 块(视觉),file 仅路径引用。
// Path 为运行时绝对路径(json 忽略;会话重放时空,适配器跳过视觉注入,保留文本引用);
// Rel 相对附件根($GAH_HOME/attachments/…)随会话 jsonl 持久化(便携:随目录迁移有效)。
type Attachment struct {
	Kind     AttachmentKind `json:"kind,omitempty"`
	Name     string         `json:"name,omitempty"`
	MimeType string         `json:"mime_type,omitempty"`
	Rel      string         `json:"rel,omitempty"`
	Path     string         `json:"-"`
}

// LLMMessage 模型对话消息。
type LLMMessage struct {
	Role        Role
	Content     string
	Attachments []Attachment
	ToolCalls   []ToolCall
	ToolCallID  string // RoleTool 时关联的工具调用 id
}

// EventLLMPreRequest 模型请求发出**之前**的 waterfall 扩展点(载荷 *LLMRequest)。
//
// 为什么需要它:`LLMRequest` 早就有 `Model`(请求级覆盖)与 `Thinking` 字段,但**没人填** ——
// 于是"按角色换模型/换思考档"这类"随场景改本回合请求"的需求无处安放。本事件是**唯一必经点**
// (`host-llm.Service.Complete` 开头,主回合/并行子代理/概述/未来调用方都走它)。
//
// 语义(waterfall):监听器可**原地改写** `*LLMRequest`(典型:当前角色声明了模型则填入);
// 返回 error = 阻断本次请求(错误上冒成调用方可见的失败)。两条纪律:
//   - 监听器必须**幂等**且只做"填空"类改写 —— 溢出兜底重试会重新走一遍请求(emit 在 Complete 内,
//     每个逻辑请求只 emit 一次;重试不重复 emit)。
//   - 监听器**不得**回调 `ctx.llm.Complete`(emit 发生在 LLM 服务发请求的路径上,回调即自锁)。
//
// 为何是常量而非字面量:发出方(host-llm)与订阅方(如 host-roles)分属不同插件,
// 两边各写一份字面量时打错一个字符就静默失效(参 EventUsageWindow 的同类先例)。
const EventLLMPreRequest = "llm/pre-request"

// LLMRequest 一次模型请求。Tools 为模型可见的工具 schema 列表。
type LLMRequest struct {
	Model       string
	Messages    []LLMMessage
	Tools       []ToolDefinition
	MaxTokens   *int
	Temperature *float64
	Thinking    ThinkingLevel // 思考等级(默认 Off=不发送;host-llm 注入会话级)
	// ThinkingSet 置真 = 调用方**显式指定**了思考档(含显式 Off):host-llm 不再用会话档覆盖。
	// 为何需要单独的布尔:`ThinkingOff` 既是零值又是合法档位 —— 只看 `Thinking == Off` 无法区分
	// "没设置"与"就是要关思考",于是"角色强制 off 压过会话 high"这类需求表达不出来。
	// 零值 false 与今天行为完全一致(既有调用方全不设 Thinking)。
	ThinkingSet bool
}

// ThinkingLevel 思考等级(推理预算)。Off=关闭(默认,不发送推理字段——对不支持端点安全);
// Low/Medium/High 由适配器映射各 API 参数(OpenAI reasoning_effort / Anthropic budget)。
type ThinkingLevel int

const (
	ThinkingOff ThinkingLevel = iota
	ThinkingLow
	ThinkingMedium
	ThinkingHigh
)

// Names 全部等级(循环切换/枚举显示用,顺序即切换顺序)。
func (ThinkingLevel) Names() []string { return []string{"off", "low", "medium", "high"} }

// Parse 解析等级名(未知 = Off)。
func ParseThinking(s string) ThinkingLevel {
	switch s {
	case "low":
		return ThinkingLow
	case "medium":
		return ThinkingMedium
	case "high":
		return ThinkingHigh
	default:
		return ThinkingOff
	}
}

// NormalizeThinking 规范化并校验思考档名(小写 + 去首尾空白;空串 = 未声明,合法)。
//
// 为何需要它:ParseThinking 对认不出的值一律静默返回 Off —— 手写的 role.yaml 里
// `thinking: High` / 带空白 / 拼错的值会被当成 off,还带“角色强制 off”语义。
// 规范化后合法档位写法(大小写/空白差异)照常通过,真正非法的值显式失败。
func NormalizeThinking(s string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return "", nil
	}
	if ParseThinking(t).String() != t {
		return "", fmt.Errorf("思考档 %q 不合法(可用 off|low|medium|high,或留空 = 跟随会话)", s)
	}
	return t, nil
}

// String 等级展示名。
func (t ThinkingLevel) String() string {
	return t.Names()[int(t)%len(t.Names())]
}

// FinishReason 结束原因。
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonLength        FinishReason = "length"
	FinishReasonContentFilter FinishReason = "content_filter"
)

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int // 缓存命中输入 token(openai prompt_tokens_details.cached_tokens / anthropic cache_read_input_tokens)
}

// UsageStats 会话级 token 消耗统计(ctx.usageStats,host-usage-stats 累计)。
type UsageStats struct {
	PromptTokens     int // 累计输入 token(整场会话求和)
	CompletionTokens int // 累计输出 token(整场会话求和)
	CachedTokens     int // 累计缓存命中输入 token
	Requests         int // 累计请求数
	// LastPromptTokens 最近一次请求的实测输入 token。
	// 为何单列:它才是**当前上下文占用**(这一次真正发出去的历史+系统提示),
	// 而累计 PromptTokens 是多轮之和 —— 拿它比窗口会越跑越离谱(多轮后出现
	// 「5000K/128K」)。展示层的「上下文 xx/xx%」一律用本字段;累计量只用于总量统计。
	LastPromptTokens int
	Window           int // 模型上下文窗口(token;context_window 配置,默认 65536)
}

// LLMStreamEvent 流式增量(文本增量 + 思维增量 + 工具调用增量;Done 时携带最终消息)。
type LLMStreamEvent struct {
	Delta        string // 文本增量
	Thinking     string // 思维增量(推理模型 reasoning_content/thinking;非空=思维段,与 Delta 互斥发送)
	ToolCallID   string // 工具调用开始/延续时提供
	ToolCallName string // 工具名 delta
	ToolCallArgs string // 参数增量
	Done         bool
	Message      LLMMessage // Done=true 时的完整消息
	FinishReason FinishReason
	Usage        Usage
}

// LLMError LLM 请求失败包装:携带请求模型名(错误文本可能含上下文窗口信息,
// host-usage-stats 订阅 agent/error 解析学习——新模型窗口自动获取的通道)。
// 保持 Unwrap,重试/取消判定不受影响。
type LLMError struct {
	Model string
	Err   error
}

func (e *LLMError) Error() string { return e.Err.Error() }
func (e *LLMError) Unwrap() error { return e.Err }

// LLMResponse 完整响应(适配器聚合流式增量后返回)。
type LLMResponse struct {
	Message      LLMMessage
	FinishReason FinishReason
	Usage        Usage
}

// LLMAdapter 模型提供商适配器。Name 为稳定标识(llm-openai-compat 等)。
// 可选接口 ModelRouter 声明支持的模型前缀(host-llm 按当前模型名路由;
// 未实现则作为默认回退适配器,对齐原 order[0] 语义)。
type LLMAdapter interface {
	Name() string
	// Complete 发起流式请求:onChunk 按增量回调;返回聚合后的完整响应。
	// 实现必须尊重 ctx 取消(取消链见设计 §8)。
	Complete(ctx context.Context, req *LLMRequest, onChunk func(ev LLMStreamEvent) error) (*LLMResponse, error)
}

// ModelRouter 可选接口:声明适配器支持的模型名/前缀(如 "claude")。
// host-llm 路由:当前模型名精确或前缀命中任一适配器声明 → 优先;
// 无命中 → 首个注册适配器(默认回退)。
type ModelRouter interface {
	Models() []string
}

// ModelCatalog 可选接口(ctx.llm 实现者按需实现):**本地**回答「这个模型名有没有适配器能接」。
// 为何单独一个窄接口而不塞进 LLMService:消费方(host-roles 在"按角色换模型"前做一次可用性校验)
// 只需要这一个问题的答案,且假实现(单测里的假 LLM)不该为此扩接口。
// 与 ModelRouter 的分工:ModelRouter 是**适配器**声明自己能接什么,Catalog 是**服务**汇总后的判定。
// 实现纪律:必须纯本地(不得打端点 /models)、不得因未知而失败(判不了就返回 true)。
// ProviderHeaders provider 级自定义请求头(键值均为字符串)。
//
// 为什么需要它:有些网关**按请求头**路由或限流,光有 base_url + key 接入不了。已知的真实
// 例子:OpenCode Go(第三方 coding agent 可用,但要求①用**自己的** user agent 标识自己、
// ②每个会话发一个**稳定**的 session ID 到 `x-opencode-session`,否则会被限速/拒绝)。
//
// 因此这一层刻意做成**通用 header 而不是某家厂商的特例**:头怎么解释由 provider 配置声明,
// 适配器只负责原样带上,不硬编码任何一家的语义。
//
// 值支持占位符(在宿主侧、发给适配器之前展开;见 providerfile.ExpandHeaders):
//
//	${session} 当前会话 id(主会话时为空 ⇒ 回落项目 key)
//	${version} gah 版本号
//	${cwd}     当前工作目录
//
// 未知占位符**原样保留**(宁可让网关看到一个它不认识的字面量,也不要静默变成空串 ——
// 后者会悄悄改变请求语义)。
type ProviderHeaders map[string]string

// HeaderConfigurable 可选接口:适配器支持 provider 级自定义请求头。
//
// 为什么不做进 LLMAdapter 接口:它只有一处消费(host-llm 切 provider 时下发),
// 而多数适配器不需要它 —— 塞进核心接口会让所有适配器为不需要的能力写空实现。
// 宿主按「实现了就下发」处理,没实现的适配器**静默忽略**并在日志留一行说明。
type HeaderConfigurable interface {
	ConfigureHeaders(h ProviderHeaders)
}

// ModelCatalog 可选接口(ctx.llm 实现者按需实现):**本地**回答「这个模型名有没有适配器能接」。
//
// 为何单独一个窄接口而不塞进 LLMService:消费方(host-roles 在"按角色换模型"前做一次可用性校验)
// 只需要这一个问题的答案,且假实现(单测里的假 LLM)不该为此扩接口。
// 实现纪律:必须纯本地(不得打端点 /models)、不得因未知而失败(判不了就返回 true)。
type ModelCatalog interface {
	KnownModel(model string) bool
}

// ModelInfo 一个可选模型(经 ListModels 从端点 /models 拉取)。
//
// 后四个字段是**可选元信息**:端点给才有值,不给就留空 —— 它们全部是判定「这个模型能不能
// 当 agent 用」的依据(见 ModelVerdict),而不是展示用的装饰。
//   - 为什么不是「统一按模型名猜」:模型名几个月一变(实测 OpenRouter 一天内 :free 清单就换了
//     一半),名字猜不出能力,端点的自述才作数。
//   - 为什么 SupportsTools 是三态(*bool):nil = 端点没说。**「没说」不等于「不支持」**,
//     把它当成 false 会把大量正常模型误判成不能用(绝大多数 provider 的 /models 不返回这个字段)。
type ModelInfo struct {
	ID      string // 模型名(如 deepseek-ai/DeepSeek-V3)
	OwnedBy string // 模型归属(如 deepseek-ai;可空)

	// ContextWindow 上下文窗口(端点自报;0 = 未知)。
	ContextWindow int
	// SupportsTools 端点是否声明支持结构化工具调用;nil = 未声明(见类型注释的三态理由)。
	SupportsTools *bool
	// InputModalities 输入模态(text/image/audio/video…;部分端点(如 OpenRouter)声明)。
	InputModalities []string
	// MaxOutputTokens 单次输出上限(端点自报;0 = 未知)。太小会让长任务在输出中途被截断。
	MaxOutputTokens int
	// PromptPrice 输入价(**每 token**,美元/单 token;0 = 免费)。
	//
	// 单位为什么写死成"每 token"而不是"每百万 token":这就是端点给的值(实测 OpenRouter 的
	// pricing.prompt = 0.00000015 对应 gpt-4o-mini,即 $0.15/M)。刻意不换算成每百万 ——
	// 换算等于把端点的口径偷偷改掉,而各家口径未必一致(有的按字符、有的按 token 打包)。
	// 命名曾经写成 PromptPricePerMillion(注释也说每百万),与实际单位不符,已改 —— 名字骗人比
	// 字段缺失更贵,因为它会让读代码的人以为拿到的数可以直接乘百万。
	//
	// 为什么要价格而不要「:free 后缀」启发:后缀只是 OpenRouter 的约定,价格才是事实
	// (实测该站 16 个 :free 模型 pricing 真为 0,但反过来不成立 —— 有 id 不带后缀的模型也可能免费)。
	PromptPrice float64
	// PriceKnown 端点是否给了价格字段(区分「0 = 免费」与「没说 = 未知」)。
	PriceKnown bool
}

// vendorHint 厂商猜测:OpenRouter 之类不返回 owned_by,只能用 id 的 `/` 前缀。
func (m ModelInfo) vendorHint() string {
	if m.OwnedBy != "" {
		return m.OwnedBy
	}
	if i := strings.IndexByte(m.ID, '/'); i > 0 {
		return m.ID[:i]
	}
	return ""
}

// SupportsVision 是否声明支持图片输入。
func (m ModelInfo) SupportsVision() bool {
	for _, mod := range m.InputModalities {
		switch strings.ToLower(strings.TrimSpace(mod)) {
		case "image", "image+text", "text+image":
			return true
		}
	}
	return false
}

// ModelLister 可选接口:适配器支持列举端点可用模型(TUI /model 动态枚举;失败回退手动)。
type ModelLister interface {
	ListModels() ([]ModelInfo, error)
}

// UsageStatsService 服务(ctx.usageStats):会话级 token 消耗统计(host-usage-stats 提供)。
// TUI 状态栏显示上下文使用率/缓存命中率;切换会话时经 Reset 归零。
type UsageStatsService interface {
	// Stats 当前会话累计统计快照。
	Stats() UsageStats
	// Reset 归零统计(切换会话时调用;新会话从零累计)。
	Reset()
}

// LLMService 服务(ctx.llm):注册适配器 + 以当前默认适配器请求。
type LLMService interface {
	// RegisterAdapter 注册适配器,返回 Disposer(卸载即撤销)。
	RegisterAdapter(a LLMAdapter) Disposer
	// SetModel 设置当前模型名(如 deepseek-chat)。
	SetModel(model string)

	// Complete 以当前默认适配器发起请求。
	// 未设置模型时返回显式错误(不静默降级)。
	Complete(ctx context.Context, req *LLMRequest, onChunk func(ev LLMStreamEvent) error) (*LLMResponse, error)

	// Model 返回当前模型名。
	Model() string

	// List 列出已注册适配器(调试/UI)。
	List() []string

	// SetProvider 运行时切换端点与凭据(作用于通用适配器,零重启)。
	// claude-* 前缀路由的适配器不受影响;无支持适配器时显式报错。
	SetProvider(baseURL, apiKey string) error

	// UnsetProvider 逐项删除配置(base_url|api_key|model),该项恢复为启动默认(env/样板)。
	UnsetProvider(field string) error

	// ResetProvider 恢复全部字段为启动默认(env/样板)。
	ResetProvider() error

	// ProviderInfo 当前通用适配器的端点与凭据(展示用;key 返回原文供打码)。
	ProviderInfo() (baseURL, apiKey string, ok bool)

	// ListModels 当前通用适配器端点可用模型列表(TUI /model 动态枚举;失败回退手动)。
	ListModels() ([]ModelInfo, error)

	// SetThinking 设置会话级思考等级(TUI Tab/Shift+Tab 循环;Complete 注入未显式设置的请求)。
	SetThinking(t ThinkingLevel)
	// Thinking 当前会话级思考等级。
	Thinking() ThinkingLevel
}

// ProviderAdapter 可选接口:适配器支持运行时端点/凭据配置(TUI /provider)。
type ProviderAdapter interface {
	// Configure 切换端点与凭据(原子生效;baseURL 校验 http(s) 前缀)。
	Configure(baseURL, apiKey string) error
	// Unset 删除某一字段(base_url|api_key|model),恢复为启动默认(env/样板)。
	Unset(field string) error
	// Reset 恢复全部字段为启动默认(env/样板)。
	Reset() error
	// ProviderInfo 返回当前端点与凭据。
	ProviderInfo() (baseURL, apiKey string)
}

// ProviderProfile 一个 provider 的运行时视图(展示/切换用;host-llm 提供)。
// ProviderProfile 一个 provider 的可序列化视图(GET /api/providers / POST 同形)。
type ProviderProfile struct {
	Name    string // 标识/切换名(域短名,持久化 provider.yaml 的 name)
	BaseURL string
	APIKey  string
	Model   string
	Active  bool // 当前活跃(adapter 端点与模型均指向它)
	// Headers provider 级自定义请求头(见 ProviderHeaders)。
	//
	// **原样回显,不打码**:设置面板本来就要回显用户自己填的配置(与 base_url 同级)。
	// 但要说清它的落盘形态 —— 与 api_key 一样**明文**存在 provider.yaml(该文件 0600),
	// 所以别把长期凭据塞进 header 值里(那种场景应该用 api_key 字段)。
	Headers ProviderHeaders
}

// ProviderHeadersMutable 可选接口(多 provider 实现者按需实现):**单独**设置某 provider 的
// 自定义请求头。
//
// 为什么不把它并进 AddProvider 的参数:AddProvider 已有四个参数且被多处实现与桩件使用
// (tests 里的 stubMultiProvider 等),加第五个参数会把所有实现与测试一起改。而「先加 provider、
// 再设头」本来就是自然的两步,拆开反而少一处耦合。宿主侧:实现了就调,没实现就明确报,
// **不静默丢弃**(头配了却没生效 = 用户以为限速规则已改,实际没改)。
type ProviderHeadersMutable interface {
	SetProviderHeaders(name string, h map[string]string) error
}

// ProviderModelList 一个 provider 端点的模型列表(/model 聚合各 provider 的结果)。
type ProviderModelList struct {
	Name    string
	BaseURL string
	Models  []ModelInfo
	Err     error // 单条拉取失败记入该条(不整体失败);空 = 成功
}

// MultiProviderService 多 provider 并存扩展(host-llm 实现;类型断言发现,LLMService 接口不变)。
type MultiProviderService interface {
	// Providers 全部 provider 运行时视图(含活跃标记)。
	Providers() []ProviderProfile
	// AddProvider 新增/更新一个 provider(同名 upsert;首个自动激活;同名更新时激活并立即生效)。
	AddProvider(name, baseURL, apiKey, model string) error
	// SetActiveProvider 切换活跃 provider(校验存在;立即 Configure 适配器并 SetModel)。
	SetActiveProvider(name string) error
	// RemoveProvider 删除一个 provider(校验存在;不存在显式报错不静默)。
	// 删的是活跃时活跃顺延到剩余首个并立即生效;删空则回退 env/样板(同 /provider clear)。
	RemoveProvider(name string) error
	// ListAllModels 聚合所有 provider 端点 /models 列表(TTL 缓存;单条失败记 Err 不整体失败)。
	ListAllModels() []ProviderModelList
}

// modelsFetchClient 模型列表直拉客户端(多 provider 聚合/非缓存路径共用;30s 超时)。
var modelsFetchClient = &http.Client{Timeout: 30 * time.Second}

// OpenAIFetchModels 直拉 openai 兼容端点 /models(不经过适配器/缓存——供 host-llm 聚合
// 非活跃端点;与适配器 ListModels 的端点语义同构)。baseURL 空 → 显式错误。
func OpenAIFetchModels(baseURL, apiKey string) ([]ModelInfo, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("openai-models: 未配置端点")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := modelsFetchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-models: 模型列表请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai-models: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	// 读整体再交给单一解析源(sdk.ParseModelsPayload):适配器自身的 ListModels 走同一条路,
	// 两边解出的元信息必须逐字一致,否则「列表里有徽标、聚合列表里没有」。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, modelsListMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("openai-models: 读取失败: %w", err)
	}
	if len(raw) > modelsListMaxBody {
		return nil, fmt.Errorf("openai-models: 模型列表响应超过 %d MiB 上限", modelsListMaxBody>>20)
	}
	infos, err := ParseModelsPayload(raw)
	if err != nil {
		return nil, fmt.Errorf("openai-models: %w", err)
	}
	return infos, nil
}

// modelsListMaxBody /models 响应上限(实测 OpenRouter 全量 400+ 模型约 1 MiB;留足余量)。
const modelsListMaxBody = 16 << 20
