package hostintcmd

// 技能包的命令面(第一百零七批):`/skill-export` 与 `/skill-import`。
//
// 与 Web 端点(/api/skillpack)**同一实现** —— 校验、包格式、上限、覆盖与回滚全在
// internal/skillpack,这里只做参数解析、路径裁决与落盘回执(与 `/export` 同款纪律:
// 命令不重新实现业务,只做"把人话翻译成调用")。
//
// 范围:只管**共享技能**。角色私有技能随角色包走(私有技能与角色的工作规则/挂载是配套的,
// 单独拆出来分享反而丢上下文)。这一点在命令说明里也写着,免得用户以为漏了。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/skillpack"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdSkillExport /skill-export <技能名> [路径] —— 缺省落当前工作区。
func (h *Host) cmdSkillExport(args []string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", errString("用法:/skill-export <技能名> [路径](技能名可在设置面板「技能库」里看)")
	}
	name := strings.TrimSpace(args[0])
	pack, err := skillpack.Export(name)
	if err != nil {
		return "", errString(err.Error())
	}
	dst := ""
	if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
		dst = strings.TrimSpace(args[1])
	} else {
		var cs sdk.CwdSessions
		if err := h.c.Inject("ctx.cwdSessions", &cs); err == nil {
			dst = cs.Path()
		}
	}
	if dst == "" {
		return "", errString("拿不到当前工作区(缺 ctx.cwdSessions),请给一个显式路径")
	}
	// 与 /export 同一道裁决:网址形态的路径显式失败(真机事故:模型把网页地址当文件路径,
	// filepath.Clean 把 "//" 折成 "/",在工作区里长出垃圾目录树)。
	if sdk.LooksLikeURLPath(dst) {
		return "", errString("导出目标是网址形态,不是本地路径:" + dst)
	}
	// 目录给全就拼建议文件名(与 Web 下载名同款:gah-skill-<名>.zip)。
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		dst = filepath.Join(dst, skillpack.FileName(name))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	if err := os.WriteFile(dst, pack, 0o644); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	return fmt.Sprintf("已导出技能 %s → %s(%d 字节;包里只有它的 SKILL.md,导入端不执行任何脚本)", name, dst, len(pack)), nil
}

// cmdSkillImport /skill-import <包路径> [--as 名] [--overwrite]
func (h *Host) cmdSkillImport(args []string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", errString("用法:/skill-import <包路径> [--as 名] [--overwrite]")
	}
	// 第一个非标志参数 = 包路径;其余按 --as / --overwrite 解析。
	// 为什么这样而不写 flag 包:命令行的解析惯例就是"位置参数 + 少量标志",
	// 为两个标志引入 flag 会带来它自己的一整套错误文案。
	path := ""
	as := ""
	overwrite := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--as="):
			as = strings.TrimSpace(strings.TrimPrefix(a, "--as="))
		case a == "--as":
			// 下一个参数是名字(在下面的循环外统一处理更啰嗦;这里用游标式扫描)
		case strings.HasPrefix(a, "--overwrite"):
			overwrite = true
		case path == "":
			path = strings.TrimSpace(a)
		}
	}
	// 处理 `--as 名字` 的分离写法
	for i, a := range args {
		if a == "--as" && i+1 < len(args) {
			as = strings.TrimSpace(args[i+1])
		}
	}
	if sdk.LooksLikeURLPath(path) {
		return "", errString("导入源是网址形态,不是本地路径:" + path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errString("读包失败: " + err.Error())
	}
	res, err := skillpack.Import(data, skillpack.ImportOptions{As: as, Overwrite: overwrite})
	if err != nil {
		return "", errString(err.Error())
	}
	// 索引跟上:导入是直接落文件,不重扫的话新技能不可见(失败只提示,不假装成功)。
	msg := fmt.Sprintf("已导入技能 %s(%d 字节)", res.Name, res.Bytes)
	if res.From != "" && res.From != res.Name {
		msg += "(原名 " + res.From + ")"
	}
	if res.Replaced {
		msg += ";覆盖了同名旧技能(旧份在回收站可恢复)"
	}
	if svc, ok := h.svc(); ok {
		if err := svc.Rescan(); err != nil {
			msg += ";注意:技能索引重扫失败(" + err.Error() + "),可 /reload 或重启 gah"
		}
	}
	return msg, nil
}

// svc 取技能服务(未装配 → nil,导入仍落盘,只是索引不刷新)。
func (h *Host) svc() (sdk.SkillsService, bool) {
	var s sdk.SkillsService
	if err := h.c.Inject("ctx.skills", &s); err != nil {
		return nil, false
	}
	return s, s != nil
}
