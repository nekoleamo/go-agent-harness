// web/plugins_install.go:插件安装/卸载/信任的 HTTP 面(2026-10-03)。
//
// 为什么要有:安装能力此前只在 CLI(`gah -install`)里 —— 但用户多数时候在 Web 或桌面壳
// 里操作,「开个终端敲命令」是断链。本文件把**同一份 internal/install 内核**接到面板上,
// 一个入口一处实现,避免「GUI 能干而 TUI 不能」的安全差异。
//
// 安全面(与 CLI、斜杠命令三处共用 internal/install 里的同一组约束):
//
//	① **审批档 strict ⇒ 拒绝**(install.GuardApproval):装插件 = 在本机引入一段会常驻
//	   执行的代码,比「改指令文件」更重 —— 与 checkInstructionFaceWrite 同一纪律。
//	② **二次确认由前端发起**(全局确认条,文案由 install.ConfirmPrompt 统一生成):
//	   服务端**不**代替用户确认,因为这一层没有可用的确认服务语义(面板确认是 UI 行为)。
//	   为此本文件只接受**已确认**的请求:前端带 `confirmed: true`,缺它一律 400。
//	   —— 这一点是刻意的:宁可要求调用方明确表态,也不让「忘了弹确认」变成静默安装。
//	③ 面板本身在本机/带 token 才可达(见 server.go 鉴权门),与能开终端是同一信任域。
package web

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// pluginHome 数据根(由 cmd/gah 统一注入 GAH_HOME;本包不自己推导,避免第二套解析链)。
func (s *Server) pluginHome() string { return sdk.Home() }

// installView 已安装插件 + 信任状态 + 审计(面板表格的一行)。
type installView struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Binary   string `json:"binary"`
	Dir      string `json:"dir"`
	// Trusted 白名单里是否有它、清单是否强制、哈希是否与盘上那份一致。
	Trusted  bool   `json:"trusted"`
	Enforced bool   `json:"enforced"`
	Hash     string `json:"hash,omitempty"`
	// Audit 最近一次登记的时间与来源(embed / install:<spec> / trust:manual)。
	AuditTime   string `json:"audit_time,omitempty"`
	AuditSource string `json:"audit_source,omitempty"`
	// Loadable 目录在但没进白名单(= 会被拒);装了但二进制不在 = 未装好。
	Loadable bool `json:"loadable"`

	// —— 批一:来源三件(仓库/ref/commit)与来源归属 ——
	//
	// 为什么单独一组而不是塞进 AuditSource:审计行回答的是「谁把这件登记进白名单的」,
	// 来源账回答的是「这插件当初是从哪一份代码装的」。后者才是用户想知道来源时真正要的答案,
	// 而且它会因为 --accept-drift / 重装而变 —— 审计行不会变。
	SourceRepo   string `json:"source_repo,omitempty"`
	SourceRef    string `json:"source_ref,omitempty"`
	SourceKind   string `json:"source_kind,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"` // 短显示
	Drifted      bool   `json:"drifted,omitempty"`
	// Origin user | official(批四的分类展示用;official = 随 gah 附带的)。
	Origin string `json:"origin,omitempty"`
	// APIVersion 装机时记下的插件协议版本(本版 gah 不认它时面板出提示条)。
	APIVersion string `json:"api_version,omitempty"`
	// CompatOK 该插件的声明版本是否在本版 gah 的支持范围内。
	CompatOK bool `json:"compat_ok"`
	// Disabled 是否被用户停用(批二)。停用 ≠ 卸载:文件还在,只是不加载。
	Disabled bool `json:"disabled"`
	// Loaded 当前是否有进程在跑。Enabled/Loaded/Disabled 三者组合起来才是面板的三态。
	Loaded bool `json:"loaded"`
}

// installSpecReq 安装请求。
type installSpecReq struct {
	Spec string `json:"spec"`
	// Confirmed 前端已完成二次确认(确认文案由 install.ConfirmPrompt 统一生成)。
	// 缺它一律拒绝:本层没有可用的确认服务,不能替用户点确认。
	Confirmed bool `json:"confirmed"`
	// Preview true = 只返回确认文案所需的事实,不安装(面板先弹确认再真装)。
	Preview bool `json:"preview"`
	// AcceptDrift 接受同名 tag 指向了新 commit(批一 §1.3)。前端只有在**先看到漂移**
	// 时才会带上它 —— 也就是它必须是用户点了之后才生效的开关。
	AcceptDrift bool `json:"accept_drift"`
	// Prebuilt 走预编译产物(批三):**不执行仓库里的构建脚本**。
	Prebuilt bool `json:"prebuilt"`
}

// installNameReq 卸载/信任类请求(只带 id)。
type installNameReq struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed"`
}

// handlePluginInstallList 已安装插件清单 + 信任状态 + 审计(GET /api/plugins/install)。
func (s *Server) handlePluginInstallList(w http.ResponseWriter, _ *http.Request) {
	home := s.pluginHome()
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		http.Error(w, "插件白名单读取失败:"+err.Error(), http.StatusInternalServerError)
		return
	}
	// 来源账读不出来(坏文件)要报错而不是当空账 —— 与 internal/install 的口径一致。
	ledger, err := install.LoadSources(home)
	if err != nil {
		http.Error(w, "插件来源账读取失败:"+err.Error(), http.StatusInternalServerError)
		return
	}
	// 启停状态来自外部插件控制面(未装配 host-bridge = 空表;面板据此不显示启停按钮)。
	live := map[string]sdk.ExternalPluginInfo{}
	if s.extp != nil {
		for _, info := range s.extp.List() {
			live[filepath.Base(info.Path)] = info
		}
	}
	out := []installView{}
	for _, it := range install.List(home) {
		v := installView{ID: it.ID, Protocol: it.Protocol, Binary: it.Binary, Dir: it.Dir,
			Enforced: list.Enforced(), CompatOK: true, Origin: install.OriginUser}
		if e, ok := ledger.Find(it.ID); ok {
			if e.Origin != "" {
				v.Origin = e.Origin
			}
			v.SourceRepo, v.SourceRef, v.SourceKind = e.Repo, e.Ref, e.Kind
			v.SourceCommit, v.Drifted, v.APIVersion = install.ShortSHA(e.Commit), e.Drifted, e.APIVersion
			v.CompatOK = e.APIVersion == "" || install.APIVersionSupported(e.APIVersion)
		}
		// 没 manifest(手工放置)时用 BinaryName 回落到扫目录 —— 与 /install 清单同一实现,
		// 两处各写一份必然会漂(而且漂了没人知道)。
		binary := it.Binary
		if binary == "" {
			binary = install.BinaryName(it.Dir)
			v.Binary = binary
		}
		binPath := filepath.Join(it.Dir, binary)
		if binary != "" {
			if info, ok := live[binary]; ok {
				v.Disabled = info.Disabled
				v.Loaded = info.Loaded
			}
			if sum, err := plugintrust.HashFile(binPath); err == nil {
				v.Loadable = true
				v.Hash = hexOfSum(sum)
				if cur, ok := list.Sum(binary); ok && cur == sum {
					v.Trusted = true
				}
				if a, ok := list.LastAuditOf(binary); ok {
					v.AuditTime, v.AuditSource = a.Time, a.Source
				}
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePluginInstall 安装(POST /api/plugins/install)。
//
// 两段式:preview=true 先取确认文案所需事实,confirmed=true 才真装。拆开是因为
// 「确认文案」需要 id / 落位目录 / 构建命令,而远端来源只有拉下来才知道 —— 与 CLI
// 侧 install.Preview 同一口径。
func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	var req installSpecReq
	if !decodeJSON(w, r, &req) {
		return
	}
	spec := strings.TrimSpace(req.Spec)
	if spec == "" {
		http.Error(w, "缺少 spec(仓库地址或本地目录)", http.StatusBadRequest)
		return
	}
	if !s.approvalAllowsInstall(w) {
		return
	}
	home := s.pluginHome()
	facts := install.Preview(spec, home)
	if req.Preview && req.Prebuilt {
		// 预编译的预览换一套文案(讲清「下载来的、没有独立校验」这件事);
		// 远端来源读不到 URL 时回落基础事实,真装时内核仍会显式报错。
		if f, ok := install.PreviewPrebuilt(spec, home); ok {
			facts = f
		}
	}
	if req.Preview {
		writeJSON(w, http.StatusOK, map[string]any{
			"prompt": install.ConfirmPrompt(facts),
			"facts":  facts,
		})
		return
	}
	if !req.Confirmed {
		http.Error(w, "未经确认:本层没有确认服务,必须由前端确认后带 confirmed=true 重发", http.StatusBadRequest)
		return
	}
	res, err := install.InstallWithOpts(spec, home,
		install.InstallOpts{AcceptDrift: req.AcceptDrift, Prebuilt: req.Prebuilt})
	if err != nil {
		http.Error(w, "安装失败:"+err.Error(), http.StatusBadRequest)
		return
	}
	// Tidied 回显给面板:补依赖意味着这个插件引入了仓库原本没声明的模块 ——
	// 供应链面被「装插件」这件事扩宽了,用户有权知道(见 internal/install/build.go ①)。
	hint := "已装上;若工具没出现,等热重载或重启 gah(宿主侧 watch 只在插件目录变动时触发)"
	if res.Tidied {
		hint += "。注意:仓库的 go.mod 不完整,构建前补跑过 go mod tidy —— 这个插件引入了仓库原本没声明的模块依赖"
	}
	if w := res.DriftWarning(); w != "" {
		hint += "。" + w
	}
	if res.Prebuilt != "" {
		hint = "已从作者发布的预编译产物装上(" + res.Prebuilt + "),**未执行任何构建命令**。" + hint
	}
	if res.BuildImplicit {
		hint += "。注意:该仓库的 plugin.yaml **没有声明 build:** —— 执行的是 gah 替你选的默认构建命令,不是作者写的"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"id":       res.ID,
		"dir":      res.Dir,
		"audit":    res.Audit,
		"tidied":   res.Tidied,
		"drifted":  res.Drifted,
		"prebuilt": res.Prebuilt,
		"record":   res.Record,
		"hint":     hint,
	})
}

// handlePluginUpdateCheck 检查已装插件的来源是否有更新(GET /api/plugins/update-check)。
//
// **只问不装**:只发 git ls-remote,不 clone、不写盘。三态(无更新/分支前移/同名 tag 被改)
// 由内核算好文案 —— 同一句话不在前端拼一次、TUI 拼一次、CLI 拼一次(拼三次就会漂)。
func (s *Server) handlePluginUpdateCheck(w http.ResponseWriter, _ *http.Request) {
	checks, err := install.CheckForUpdates(s.pluginHome())
	if err != nil {
		http.Error(w, "检查失败:"+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checks": checks,
		"notice": install.CompatibilityNoticeText(s.pluginHome()),
		"hint":   "这里只检查、不会自动安装;要装新版就重新执行安装命令。",
	})
}

// handlePluginUninstall 卸载(POST /api/plugins/uninstall)。
func (s *Server) handlePluginUninstall(w http.ResponseWriter, r *http.Request) {
	var req installNameReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "缺少 id", http.StatusBadRequest)
		return
	}
	if !s.approvalAllowsInstall(w) {
		return
	}
	if !req.Confirmed {
		http.Error(w, "未经确认(卸载同样要你点头:白名单条目会一并撤销)", http.StatusBadRequest)
		return
	}
	// ctl 传进去才能**先停进程再删文件**(批二 §2.7)。extp 为 nil = 该 profile 没装
	// host-bridge(没有活进程可停),不是错误 —— 与 CLI 场景同款。
	if err := install.Uninstall(req.ID, s.pluginHome(), s.extp); err != nil {
		http.Error(w, "卸载失败:"+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePluginDisable / handlePluginEnable 停用与启用(POST /api/plugins/install/disable|enable)。
//
// 路由为什么不是 `/api/plugins/{id}/disable`:那个命名空间已经被**进程内**插件的
// `/load` / `/unload` 占了(见 pluginToggle)。两个语义不同的动作共用一段路径,
// 迟早有人把其中一个接到另一个的 handler 上。启停与安装/卸载同属一条生命周期,
// 故并到 install 命名空间里。
//
// **停用 ≠ 卸载**:前者留文件与两个账条目(重新启用无需重新 trust),后者全删。
// 面板上按钮文案不同,卸载必须二次确认。
func (s *Server) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	s.pluginToggleState(w, r, false)
}

func (s *Server) handlePluginEnable(w http.ResponseWriter, r *http.Request) {
	s.pluginToggleState(w, r, true)
}

func (s *Server) pluginToggleState(w http.ResponseWriter, r *http.Request, enable bool) {
	var req installNameReq
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.ID)
	if name == "" {
		http.Error(w, "缺少 id", http.StatusBadRequest)
		return
	}
	verb := "停用"
	if enable {
		verb = "启用"
	}
	if !req.Confirmed {
		http.Error(w, "未经确认("+verb+"外部插件是你在决定本机常驻执行什么)", http.StatusBadRequest)
		return
	}
	if s.extp == nil {
		http.Error(w, "该 profile 未装配外部插件控制面(没有 host-bridge),无法"+verb, http.StatusBadRequest)
		return
	}
	var err error
	if enable {
		err = s.extp.Enable(name)
	} else {
		err = s.extp.Disable(name)
	}
	if err != nil {
		http.Error(w, verb+"失败:"+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enable})
}

// handlePluginTrust / handlePluginUntrust 白名单登记与撤销(POST /api/plugins/trust|untrust)。
func (s *Server) handlePluginTrust(w http.ResponseWriter, r *http.Request) {
	s.trustToggle(w, r, true)
}

func (s *Server) handlePluginUntrust(w http.ResponseWriter, r *http.Request) {
	s.trustToggle(w, r, false)
}

func (s *Server) trustToggle(w http.ResponseWriter, r *http.Request, trust bool) {
	var req installNameReq
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.ID)
	if name == "" {
		http.Error(w, "缺少插件名", http.StatusBadRequest)
		return
	}
	if !req.Confirmed {
		http.Error(w, "未经确认(白名单是安全边界,变更必须你点头)", http.StatusBadRequest)
		return
	}
	home := s.pluginHome()
	if trust {
		if err := install.Trust(name, home); err != nil {
			http.Error(w, "登记失败:"+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := install.Untrust(name, home); err != nil {
			http.Error(w, "撤销失败:"+err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// approvalAllowsInstall 审批档门(strict ⇒ 403);未装配审批服务 ⇒ 放行。
//
// 未装配时放行而不是拒绝:极简 profile(headless/mcp-serve)里没有审批档这回事,
// 让「装配与否」决定能不能装插件,会让 headless 的插件管理凭空不可用。
func (s *Server) approvalAllowsInstall(w http.ResponseWriter) bool {
	if s.ap == nil {
		return true
	}
	mode := s.ap.Mode()
	if es, ok := s.ap.(sdk.EffectiveApproval); ok {
		mode = es.EffectiveMode()
	}
	if err := install.GuardApproval(mode); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return false
	}
	return true
}

// decodeJSON 读 JSON 请求体(空体 = 零值,不报错 —— 前端首次请求可能不带体)。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "请求体解析失败:"+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// hexOfSum [32]byte → 64 位小写 hex(面板展示与比对用)。
func hexOfSum(s [32]byte) string { return hex.EncodeToString(s[:]) }

// handleUIDisable / handleUIEnable UI 插件的停用与启用(批四 §A.4)。
//
// **CLI 本批不做**:启停的主要使用面是面板,再加三个旗标不值得。
// 与外部进程插件同样的取舍 —— 那边的 CLI 出口有(用户要在终端里止损),这边的没有。
//
// 停用的效果是**不下发**(scanUIPlugins 过滤)⇒ 前端根本不加载它,而不是「加载了再藏起来」。
func (s *Server) handleUIDisable(w http.ResponseWriter, r *http.Request) {
	s.uiToggleState(w, r, false)
}

func (s *Server) handleUIEnable(w http.ResponseWriter, r *http.Request) {
	s.uiToggleState(w, r, true)
}

func (s *Server) uiToggleState(w http.ResponseWriter, r *http.Request, enable bool) {
	var req installNameReq
	if !decodeJSON(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		http.Error(w, "缺少 id", http.StatusBadRequest)
		return
	}
	verb := "停用"
	if enable {
		verb = "启用"
	}
	if !req.Confirmed {
		http.Error(w, "未经确认("+verb+"UI 插件是你在决定主页面加载谁的代码 —— UI 插件与宿主同源同权限)", http.StatusBadRequest)
		return
	}
	prefs.SetUIDisabled(id, !enable)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enable})
}

// handleUIPluginState UI 插件的**状态事实**(GET /api/ui-plugins/state)。
//
// 两个字段回答两件面板必须知道的事:
//   - disabled:被停用的 id。停用项**不在** /api/ui-plugins 里(那是"下发什么"),
//     而「我停用的那个」必须仍然看得见 —— 它从清单里消失,等于用户以为自己停错了;
//   - enforced:完整性闸是否强制。批四起 UI 侧**默认强制**(boot 无条件创建),
//     所以面板要能无条件告诉用户「手工放置的不会被加载,放行命令是 X」。
//     它从 /api/ui-plugins 的返回里拿不到:闸挡住时那个数组是**空的**。
func (s *Server) handleUIPluginState(w http.ResponseWriter, _ *http.Request) {
	root := s.cfg.UIPluginsDir
	enforced := false
	if root != "" {
		if list, err := plugintrust.Load(root); err == nil {
			enforced = list.Enforced()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"disabled": s.uiDisabledIDs(),
		"enforced": enforced,
		"note":     "UI 插件与宿主同源同权限(能调全部 API,含工具执行)。手工放进 ui-plugins/ 的默认不加载;放行: gah -trust-ui-plugin <id>",
	})
}

func (s *Server) uiDisabledIDs() []string {
	out := append([]string(nil), prefs.Load().UIDisabled...)
	sort.Strings(out)
	return out
}
