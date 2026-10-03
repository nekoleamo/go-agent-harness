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
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
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
}

// installSpecReq 安装请求。
type installSpecReq struct {
	Spec string `json:"spec"`
	// Confirmed 前端已完成二次确认(确认文案由 install.ConfirmPrompt 统一生成)。
	// 缺它一律拒绝:本层没有可用的确认服务,不能替用户点确认。
	Confirmed bool `json:"confirmed"`
	// Preview true = 只返回确认文案所需的事实,不安装(面板先弹确认再真装)。
	Preview bool `json:"preview"`
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
	out := []installView{}
	for _, it := range install.List(home) {
		v := installView{
			ID: it.ID, Protocol: it.Protocol, Binary: it.Binary, Dir: it.Dir,
			Enforced: list.Enforced(),
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
	res, err := install.Install(spec, home)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"id":     res.ID,
		"dir":    res.Dir,
		"audit":  res.Audit,
		"tidied": res.Tidied,
		"hint":   hint,
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
	if err := install.Uninstall(req.ID, s.pluginHome()); err != nil {
		http.Error(w, "卸载失败:"+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
