// web 记忆治理端点(第一百一十批 · 补齐面板侧)。
//
// 与 `/memory` 命令**同一份实现**:两边都只调 `ctx.memory`(sdk.MemoryService),
// 落盘细节在 internal/memory。面板不是第二套记忆实现 —— 否则「命令删了一条、
// 面板还显示着」这类不一致迟早出现。
//
// 未装配 ctx.memory(host-memory 未启用)→ 503,前端据此隐藏「记忆」段(不摆空壳)。
//
// 能力边界(**刻意不在这批扩**):写入只到**用户级**。项目级记忆当前只有只读展示
// (`ListProject`),要能写就得先扩 internal/memory + 命令侧,那是另一批 —— 面板不
// 偷偷比命令多一块能力(那会让「命令能做的」与「面板能做的」长期分叉)。
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// memorySvcErr 记忆服务缺失(503)。独立出来是为了让两处端点给出同一句可读原因。
var errNoMemory = errors.New("记忆服务未装配(缺 ctx.memory / host-memory 插件)")

// memoryService 懒解析记忆服务(可能为 nil,不报错;调用方自行决定是否 503)。
// 理由与 roleService 相同:ui-web-app 与 host-memory 无拓扑依赖,启动期一次性
// Inject 会恒为 nil。
func (s *Server) memoryService() sdk.MemoryService {
	s.roleMu.Lock()
	cached := s.memory
	s.roleMu.Unlock()
	if cached != nil {
		return cached
	}
	if s.ctx == nil {
		return nil
	}
	var ms sdk.MemoryService
	if err := s.ctx.Inject("ctx.memory", &ms); err != nil || ms == nil {
		return nil
	}
	s.roleMu.Lock()
	s.memory = ms
	s.roleMu.Unlock()
	return ms
}

// memorySvc 取记忆服务(缺则 503 并写响应)。
func (s *Server) memorySvc(w http.ResponseWriter) (sdk.MemoryService, bool) {
	if ms := s.memoryService(); ms != nil {
		return ms, true
	}
	http.Error(w, errNoMemory.Error(), http.StatusServiceUnavailable)
	return nil, false
}

// projectMemory 可选扩展:项目级记忆的只读列表(未实现时面板不显示该组,
// 而不是显示一个空的「项目记忆」骗人)。
type projectMemory interface{ ListProject() []string }

// memoryCandidateSvc 取候选能力(未实现 ⇒ nil,**不报错**):候选是可选能力,
// 老构建没有它时面板只是少一块,不该整段 503。
func memoryCandidateSvc(ms sdk.MemoryService) sdk.MemoryCandidates {
	cs, _ := ms.(sdk.MemoryCandidates)
	return cs
}

// memoryView GET /api/memory 的响应体。
type memoryView struct {
	Enabled bool     `json:"enabled"`
	Budget  int      `json:"budget"`
	User    []string `json:"user"`              // 展示行(新的在前,带序号与来源标注)
	Project []string `json:"project,omitempty"` // 未实现可选扩展时省略,前端不渲染该组
	// 路径:记忆是纯 markdown 文件,**设计上就允许人手改** —— 面板给出路径比藏着好。
	UserPath    string `json:"user_path"`
	ProjectPath string `json:"project_path,omitempty"`
	ProjectKey  string `json:"project_key,omitempty"`
	// 候选池(M2 前置件):**候选永不进上下文**,只有 accept 之后才进。
	// 该能力未实现(旧构建)时整组省略,前端不渲染候选区。
	Candidates        []string `json:"candidates,omitempty"`
	CandidatePath     string   `json:"candidate_path,omitempty"`
	CandidateUsed     int      `json:"candidate_used"`
	CandidateLimit    int      `json:"candidate_limit"`
	CandidateToday    int      `json:"candidate_today"`
	CandidateTodayMax int      `json:"candidate_today_max"`
}

// handleMemory GET /api/memory(只读视图)/ POST /api/memory(四个动作)。
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.memorySvc(w)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		s.memoryView(w, ms, nil)
		return
	}
	var body struct {
		Action  string `json:"action"`
		Content string `json:"content"`
		Index   int    `json:"index"`
		Source  string `json:"source"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	var deleted any // 仅删除类动作有值:面板要显示「删了 N 条」,而不是只刷新列表
	switch body.Action {
	case "add":
		if strings.TrimSpace(body.Content) == "" {
			http.Error(w, "记忆内容为空(写一句「是什么、为什么」即可)", http.StatusBadRequest)
			return
		}
		if err := ms.Add(body.Content, ""); err != nil {
			http.Error(w, "写入失败:"+err.Error(), http.StatusInternalServerError)
			return
		}
	case "remove":
		// 序号越界是**用户可见的错**(面板的序号来自上一次 GET,中间可能被命令改过),
		// 属内容问题 400,不当环境问题 500。
		if _, err := ms.Remove(body.Index); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		deleted = 1
	case "remove_source":
		if strings.TrimSpace(body.Source) == "" {
			http.Error(w, "缺少 source(要按来源会话删)", http.StatusBadRequest)
			return
		}
		n, err := ms.RemoveBySource(body.Source)
		if err != nil {
			http.Error(w, "删除失败:"+err.Error(), http.StatusInternalServerError)
			return
		}
		// 「删了 0 条」也如实回 200 + deleted:0 —— 面板据此显示「没有来自该会话的记忆」,
		// 而不是报成功却什么都不发生。
		deleted = n
	case "toggle":
		if body.Enabled == nil {
			http.Error(w, "toggle 需要 enabled 字段", http.StatusBadRequest)
			return
		}
		ms.SetEnabled(*body.Enabled)
	case "propose":
		// 候选池是**可选能力**:没装配就 501(如实说"这一版没有候选功能"),
		// 绝不能退化成"直接写进记忆"—— 那会让面板比命令多一块能力,而用户以为走的是候选。
		cs := memoryCandidateSvc(ms)
		if cs == nil {
			http.Error(w, "该构建不支持候选池(缺 host-memory 的候选能力)", http.StatusNotImplemented)
			return
		}
		if strings.TrimSpace(body.Content) == "" {
			http.Error(w, "候选内容为空", http.StatusBadRequest)
			return
		}
		// 限流在这里显式报错(4xx:「你今天提太多」是内容问题,不是环境坏了)
		if err := cs.Propose(body.Content, ""); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "accept", "reject", "accept_all", "reject_all":
		cs := memoryCandidateSvc(ms)
		if cs == nil {
			http.Error(w, "该构建不支持候选池(缺 host-memory 的候选能力)", http.StatusNotImplemented)
			return
		}
		switch body.Action {
		case "accept":
			if _, err := cs.AcceptCandidate(body.Index); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			deleted = 0
		case "reject":
			if _, err := cs.RejectCandidate(body.Index); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			deleted = 0
		case "accept_all":
			n, err := cs.AcceptAllCandidates()
			if err != nil {
				// 半截状态必须说清楚:已转正几条 + 错在哪(不能报「都好了」)
				http.Error(w, fmt.Sprintf("已转正 %d 条后出错(不是全成):%s", n, err), http.StatusInternalServerError)
				return
			}
			deleted = n
		case "reject_all":
			n, err := cs.RejectAllCandidates()
			if err != nil {
				http.Error(w, "驳回失败:"+err.Error(), http.StatusInternalServerError)
				return
			}
			deleted = n
		}
	default:
		http.Error(w, "未知 action:"+body.Action, http.StatusBadRequest)
		return
	}
	s.memoryView(w, ms, map[string]any{"deleted": deleted})
}

// memoryView 写只读视图(写动作完成后回同一形状,面板刷新即可;extra 并进响应)。
func (s *Server) memoryView(w http.ResponseWriter, ms sdk.MemoryService, extra map[string]any) {
	v := memoryView{
		Enabled:    ms.Enabled(),
		Budget:     ms.Budget(),
		User:       ms.List(),
		UserPath:   memory.UserPath(),
		ProjectKey: s.memoryProjectKey(),
	}
	if v.User == nil {
		v.User = []string{} // 空清单回 [] 不回 null(与角色/技能段同口径)
	}
	if pm, ok := ms.(projectMemory); ok {
		v.Project = pm.ListProject()
		if v.Project == nil {
			v.Project = []string{}
		}
	}
	if v.ProjectKey != "" {
		v.ProjectPath = memory.ProjectPath(v.ProjectKey)
	}
	if cs := memoryCandidateSvc(ms); cs != nil {
		v.Candidates = cs.ListCandidates()
		if v.Candidates == nil {
			v.Candidates = []string{}
		}
		v.CandidatePath = memory.CandidatesPath()
		v.CandidateUsed, v.CandidateLimit, v.CandidateToday, v.CandidateTodayMax = cs.CandidateQuota()
	}
	if len(extra) == 0 {
		writeJSON(w, http.StatusOK, v)
		return
	}
	// 并进附加字段:先转 map 再写(不引第三方库;形状扁平,手工合并够用)。
	raw, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "序列化失败", http.StatusInternalServerError)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		http.Error(w, "序列化失败", http.StatusInternalServerError)
		return
	}
	for k, val := range extra {
		m[k] = val
	}
	writeJSON(w, http.StatusOK, m)
}

// memoryProjectKey 当前打开会话的项目键(拿不到 = 只显示用户级,不编一个 key)。
func (s *Server) memoryProjectKey() string {
	if s.ctx == nil {
		return ""
	}
	var cs sdk.CwdSessions
	if err := s.ctx.Inject("ctx.cwdSessions", &cs); err != nil || cs == nil {
		return ""
	}
	return cs.Current()
}
