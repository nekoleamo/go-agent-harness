// MCP server 配置端点(NOND-M1 第 2/3 步「按 server 门控 + GUI 管理 + 热重载」):
//
//	GET  /api/mcp → 配置视图(来源/模式/启停 + 运行期状态:是否已加载/工具数)
//	POST /api/mcp → 保存(写 $GAH_HOME/config/mcp.yaml)+ 重启 tool-mcp 外部插件使其重读
//
// 配置模型与持久化在 internal/mcpconfig(宿主与外部进程**共用同一份**读写实现,
// 避免两套 schema 漂移);运行期状态从两处推导 —— direct 模式看已注册工具名
// (mcp_<server>_*),search 模式看 mcp_search 的索引(它的空查询 = 全量清单)。
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
)

// mcpPluginName 承接 MCP server 配置的外部插件名(目录名;见 host-bridge Reload)。
const mcpPluginName = "tool-mcp"

// mcpServerView 一项 MCP server 的配置 + 运行期状态。
type mcpServerView struct {
	mcpconfig.Server
	Loaded bool `json:"loaded"`
	Tools  int  `json:"tools"`
}

// handleMCPList MCP 配置视图(GET /api/mcp)。
func (s *Server) handleMCPList(w http.ResponseWriter, _ *http.Request) {
	view, err := s.mcpView()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// mcpSaveReq 保存请求:servers = 完整列表(UI 侧增删改后整体提交);
// Reload 缺省 true(保存即生效);显式 false = 只写盘不重启插件。
type mcpSaveReq struct {
	Servers []mcpconfig.Server `json:"servers"`
	Reload  *bool              `json:"reload"`
}

// handleMCPSave 保存 MCP 配置(POST /api/mcp)。
// 写盘成功但插件重启失败 → 200 + reload_err(部分成功必须让用户看见,不伪装成失败)。
func (s *Server) handleMCPSave(w http.ResponseWriter, r *http.Request) {
	var req mcpSaveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if err := mcpconfig.Save(req.Servers); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reload := req.Reload == nil || *req.Reload
	reloadErr := ""
	if reload {
		if s.extp == nil {
			reloadErr = "未装配外部插件控制面(ctx.extplugins):配置已保存,重启 gah 后生效"
		} else if rerr := s.extp.Reload(mcpPluginName); rerr != nil {
			reloadErr = "配置已保存,但重载 " + mcpPluginName + " 失败: " + rerr.Error()
		}
	}
	// 视图必须在重载**之后**组装:重载前取到的是旧工具面,面板会显示「已保存但 0 个工具/未生效」,
	// 用户会当成保存失败(实机实测:保存后插件其实已连上,日志里有「已连接:1 个工具」)。
	view, err := s.mcpView()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if reloadErr != "" {
		view["reload_err"] = reloadErr
	}
	writeJSON(w, http.StatusOK, view)
}

// mcpView 组装配置视图(配置来自文件 ∪ env;状态来自运行期工具面)。
func (s *Server) mcpView() (map[string]any, error) {
	servers, notes, err := mcpconfig.Load()
	if err != nil {
		return nil, err
	}
	names := s.registeredToolNames()
	searchCount, searchTotal, searchable := s.searchIndexCounts(names)

	views := make([]mcpServerView, 0, len(servers))
	attributed := map[string]bool{}
	for _, sp := range servers {
		v := mcpServerView{Server: sp}
		if sp.ModeOrDefault() == mcpconfig.ModeSearch {
			// search 模式:工具不在注册表里,计数取自 mcp_search 索引(按 server 分组)。
			// 注:server 连上但零工具时与"未加载"不可区分(与 direct 模式同一取舍)。
			v.Tools = searchCount[sp.Name]
			v.Loaded = v.Tools > 0
		} else {
			prefix := "mcp_"
			if sp.Name != "" {
				prefix = "mcp_" + sp.Name + "_"
			}
			for _, n := range names {
				if n == "mcp_search" || n == "mcp_call" {
					continue
				}
				if strings.HasPrefix(n, prefix) {
					if sp.Name == "" {
						attributed[n] = true
					}
					v.Tools++
				}
			}
			v.Loaded = v.Tools > 0
		}
		views = append(views, v)
	}
	// 无名(单 server 兼容)条目:只统计未被具名 server 前缀吃掉的 mcp_* 工具
	for i := range views {
		if views[i].Name != "" || views[i].ModeOrDefault() == mcpconfig.ModeSearch {
			continue
		}
		n := 0
		for _, name := range names {
			if name == "mcp_search" || name == "mcp_call" || attributed[name] {
				continue
			}
			if strings.HasPrefix(name, "mcp_") {
				n++
			}
		}
		views[i].Tools, views[i].Loaded = n, n > 0
	}

	pluginLoaded := len(searchCount) > 0 || searchable
	for _, v := range views {
		if v.Loaded {
			pluginLoaded = true
			break
		}
	}
	out := map[string]any{
		"path":             mcpconfig.Path(),
		"servers":          views,
		"reload_available": s.extp != nil,
		// plugin_loaded = 是否检测到 tool-mcp 已加载(未加载时 UI 提示需重启/查日志)
		"plugin_loaded": pluginLoaded,
	}
	if searchTotal > 0 {
		out["search_total"] = searchTotal
	}
	if len(notes) > 0 {
		out["notes"] = notes
	}
	return out, nil
}

// registeredToolNames 当前注册的工具名(未装配 ctx.tools = 空)。
func (s *Server) registeredToolNames() []string {
	if s.tools == nil {
		return nil
	}
	defs := s.tools.List()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// searchIndexCounts 经 mcp_search 空查询取 search 模式索引:server → 工具数。
// 返回 (每 server 计数, 索引总条数, mcp_search 是否已注册)。
// mcp_search 只出现在有 search 模式 server 时;未注册 = 无 search 模式(或插件未加载)。
func (s *Server) searchIndexCounts(names []string) (map[string]int, int, bool) {
	counts := map[string]int{}
	found := false
	for _, n := range names {
		if n == "mcp_search" {
			found = true
		}
	}
	if !found || s.tools == nil {
		return counts, 0, found
	}
	res, err := s.tools.Execute(context.Background(), "mcp_search", `{"query":"","limit":100}`)
	if err != nil || res.Error != "" {
		return counts, 0, true
	}
	var parsed struct {
		Tools []struct {
			Server string `json:"server"`
		} `json:"tools"`
	}
	if json.Unmarshal([]byte(res.Content), &parsed) != nil {
		return counts, 0, true
	}
	for _, t := range parsed.Tools {
		counts[t.Server]++
	}
	return counts, len(parsed.Tools), true
}
