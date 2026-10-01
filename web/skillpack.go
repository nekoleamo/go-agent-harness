// web 技能包端点(第一百零六批):`/api/skillpack` —— 共享技能的单文件导出/导入。
//
// 为什么是**独立前缀**而不是挂在 /api/skills 下:与 `/api/rolepack` 同一课
// (见 web/rolepack.go 头部)—— 技能名的合法字符里就有 `import`/`export`,
// 挂 `/api/skills/{name}/export` 会被通配路由吃掉。独立段最省事也不再撞。
//
// 与角色包的分工:角色包导出**已经带着角色的私有技能**,所以本端点只管**共享技能**
// —— 两条路合起来技能分享没有缺口,不必在这里重复"导一个角色"。
//
// 两条入口一份实现:校验/包格式/覆盖/回滚全在 internal/skillpack,这里只做
// HTTP 语义(下载头 / multipart / 状态码)与导入后让技能索引跟上(Rescan)。
package web

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/skillpack"
)

// maxSkillPackUpload 上传体上限(包自身上限 + 64 KiB 给 multipart 边界与头;
// 别让一个超大 body 先灌进内存)。
const maxSkillPackUpload = skillpack.MaxPackBytes + (64 << 10)

// handleSkillPackGet GET /api/skillpack/{name} —— 下载技能包(zip 附件)。
func (s *Server) handleSkillPackGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.skillsSvc(w); !ok {
		return
	}
	name := r.PathValue("name")
	pack, err := skillpack.Export(name)
	if err != nil {
		// 不存在与包坏了都归 400:最终都是 Export 报的错(读不到文件),分 404 没有意义。
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 文件名由服务端给定(名字已过 skills.ValidateName:不含引号与斜杠)。
	fname := skillpack.FileName(name)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(pack)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pack)
}

// handleSkillPackPost POST /api/skillpack?as=<名>&overwrite=1 —— 导入技能包(multipart "file")。
func (s *Server) handleSkillPackPost(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.skillsSvc(w)
	if !ok {
		return
	}
	data, msg := readSkillPackUpload(r)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1" || r.URL.Query().Get("overwrite") == "true"
	res, err := skillpack.Import(data, skillpack.ImportOptions{
		As:        strings.TrimSpace(r.URL.Query().Get("as")),
		Overwrite: overwrite,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 索引跟上:导入是直接落文件,不重扫的话新技能不可见(重扫失败如实回 warning,
	// 不假装成功 —— 与角色包端点同款口径)。
	if err := svc.Rescan(); err != nil {
		s.log.Error("技能包导入后重扫失败", "name", res.Name, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res,
			"warning": "技能包已写入,但技能索引重扫失败: " + err.Error() + "(可 /reload 或重启 gah)"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

// readSkillPackUpload 取 multipart 里的 "file" 字段(不落临时盘:技能包本就只有
// 一个 SKILL.md,读进内存即可;大小上限在 skillpack 里还有一道)。
// 返回非空 msg 即面向用户的 400 文案。
func readSkillPackUpload(r *http.Request) ([]byte, string) {
	if r.ContentLength > maxSkillPackUpload {
		return nil, "技能包超上限"
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "请求须为 multipart/form-data 且带 file 字段"
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, "请求里没有 file 字段"
		}
		if err != nil {
			return nil, "multipart 解析失败: " + err.Error()
		}
		if part.FormName() != "file" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxSkillPackUpload+1))
		if err != nil {
			return nil, "读取上传内容失败: " + err.Error()
		}
		if int64(len(data)) > maxSkillPackUpload {
			return nil, "技能包超上限"
		}
		if len(data) == 0 {
			return nil, "文件是空的"
		}
		return data, ""
	}
}
