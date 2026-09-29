// web 角色包端点(第九十三批):`/api/rolepack` —— 角色的单文件导出/导入。
//
// 为什么是**独立前缀**而不是挂在 /api/roles 下(与回收站同一课):
// 角色 ID 的合法值包含 `export`/`import`(小写字母就行)—— 挂在 `/api/roles/{id}/export`
// 会被通配路由吃掉(第八十三批的 `trash` 技能名就是同一坑)。独立段最省事也不再撞。
//
// 两条入口一份实现:`/role export|import` 命令与这里都只调 internal/rolepack,
// 校验/包格式/覆盖/回滚全在一处;这里只多两件事 —— HTTP 语义(下载头 / multipart / 状态码)
// 与导入后让角色索引跟上(Reload;失败走 200 + warning,不假装成功)。
package web

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/rolepack"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// maxPackUpload 上传体上限(rolepack 内部对包有自己的上限,这里只是别让一个超大 body
// 先灌进内存;+64 KiB 给 multipart 边界与头留余量)。
const maxPackUpload = rolepack.MaxPackBytes + (64 << 10)

// handleRolePackGet GET /api/rolepack/{id} —— 下载角色包(zip 附件)。
func (s *Server) handleRolePackGet(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, found := svc.Get(id); !found {
		http.Error(w, "角色不存在: "+id, http.StatusNotFound)
		return
	}
	pack, err := rolepack.Export(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 文件名由服务端给定(id 已过 ValidateID:小写字母/数字/连字符,不含引号与斜杠)。
	name := rolepack.FileName(id)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(pack)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pack)
}

// handleRolePackPost POST /api/rolepack?as=<id>&overwrite=1 —— 导入角色包(multipart "file")。
func (s *Server) handleRolePackPost(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	if r.ContentLength > maxPackUpload {
		http.Error(w, "角色包超上限", http.StatusRequestEntityTooLarge)
		return
	}
	data, msg := readPackUpload(r)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1" || r.URL.Query().Get("overwrite") == "true"
	res, err := rolepack.Import(data, rolepack.ImportOptions{
		As:        strings.TrimSpace(r.URL.Query().Get("as")),
		Overwrite: overwrite,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 索引跟上:导入是直接落文件,不重载的话新角色看不见(重载失败如实回 warning)
	if rr, ok := svc.(sdk.ReloadableRoles); ok {
		if err := rr.ReloadRoles(); err != nil {
			s.log.Error("角色包导入后重载失败", "id", res.ID, "err", err)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res,
				"warning": "角色包已写入,但角色索引重载失败: " + err.Error() + "(可 /reload 或重启 gah)"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

// readPackUpload 取 multipart 里的第一个 "file" 字段(不落临时盘:角色包本就该很小,
// 读进内存即可;大小上限在 rolepack 里还有一道)。返回非空 msg 即面向用户的 400 文案。
func readPackUpload(r *http.Request) ([]byte, string) {
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
		data, err := io.ReadAll(io.LimitReader(part, maxPackUpload+1))
		if err != nil {
			return nil, "读取上传内容失败: " + err.Error()
		}
		if int64(len(data)) > maxPackUpload {
			return nil, "角色包超上限"
		}
		return data, ""
	}
}
