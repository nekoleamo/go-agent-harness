package hostintcmd

// 角色包的命令面(第一百零八批):`/role-pack export` 与 `/role-pack import`。
//
// 为什么现在才补:技能包命令面(第一百零七批)做完,发现**角色包一直只有 Web 端点**
// —— TUI/headless 用户想分享角色只能回设置面板。两条入口一份实现(与 /api/rolepack 同款):
// 校验、包格式、覆盖与回滚全在 internal/rolepack,命令只做参数解析、路径裁决与落盘回执。
//
// 与技能包命令面的差别(别照抄错):
//   - 角色包是**目录形态**(定义 + 工作规则 + 私有技能),包更大 ⇒ 落盘前查上限;
//   - 导入后要 **ReloadRoles**(角色索引),不是 Rescan(技能索引);
//   - 覆盖时旧份进 `roles/.trash`(回执里的 backup_name 就是它的条目名)。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/rolepack"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdRolePackExport /role-pack export <角色id> [路径] —— 缺省落当前工作区。
func (h *Host) cmdRolePackExport(args []string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", errString("用法:/role-pack export <角色标识> [路径](标识可在设置面板「角色」段看)")
	}
	id := strings.TrimSpace(args[0])
	pack, err := rolepack.Export(id)
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
	if sdk.LooksLikeURLPath(dst) {
		return "", errString("导出目标是网址形态,不是本地路径:" + dst)
	}
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		dst = filepath.Join(dst, rolepack.FileName(id))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	if err := os.WriteFile(dst, pack, 0o644); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	return fmt.Sprintf("已导出角色 %s → %s(%d 字节;含定义 + 工作规则 + 私有技能)", id, dst, len(pack)), nil
}

// cmdRolePackImport /role-pack import <包路径> [--as 名] [--overwrite]
func (h *Host) cmdRolePackImport(args []string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", errString("用法:/role-pack import <包路径> [--as 名] [--overwrite]")
	}
	path, as, overwrite := "", "", false
	for i, a := range args {
		switch {
		case strings.HasPrefix(a, "--as="):
			as = strings.TrimSpace(strings.TrimPrefix(a, "--as="))
		case a == "--as":
			if i+1 < len(args) {
				as = strings.TrimSpace(args[i+1])
			}
		case strings.HasPrefix(a, "--overwrite"):
			overwrite = true
		case path == "":
			path = strings.TrimSpace(a)
		}
	}
	if sdk.LooksLikeURLPath(path) {
		return "", errString("导入源是网址形态,不是本地路径:" + path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", errString("读包失败: " + err.Error())
	}
	// 先查大小再读进内存:角色包含目录形态(定义 + 规则 + 私有技能),上限比技能包大一号,
	// 但仍不该让一个误传的整盘文件灌进内存。
	if fi.Size() > rolepack.MaxPackBytes {
		return "", errString(fmt.Sprintf("包 %d 字节,超过上限 %d", fi.Size(), rolepack.MaxPackBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errString("读包失败: " + err.Error())
	}
	res, err := rolepack.Import(data, rolepack.ImportOptions{As: as, Overwrite: overwrite})
	if err != nil {
		return "", errString(err.Error())
	}
	msg := fmt.Sprintf("已导入角色 %s", res.ID)
	if res.Name != "" && res.Name != res.ID {
		msg += "(" + res.Name + ")"
	}
	if n := len(res.Skills); n > 0 {
		msg += fmt.Sprintf(",含私有技能 %d 个", n)
	}
	if res.Replaced && res.BackupName != "" {
		msg += ";覆盖了同名旧角色(旧份在回收站:" + res.BackupName + ")"
	}
	// 索引跟上:导入是直接落文件,不重载的话新角色在 /role 列表里看不见。
	// 未装配角色服务(该构建没装 host-roles)时不做重载 —— 导入仍已落盘,如实提示即可。
	var rolesSvc sdk.RoleService
	if err := h.c.Inject("ctx.roles", &rolesSvc); err == nil {
		if rr, ok := rolesSvc.(sdk.ReloadableRoles); ok {
			if err := rr.ReloadRoles(); err != nil {
				msg += ";注意:角色索引重载失败(" + err.Error() + "),可 /reload 或重启 gah"
			}
		}
	} else {
		msg += ";注意:当前构建未装配角色服务,新角色要重启 gah 后可见"
	}
	return msg, nil
}

// cmdRolePack /role-pack export|import … —— 子命令分派(照 /session 的形状)。
func (h *Host) cmdRolePack(args []string) (string, error) {
	if len(args) == 0 {
		return "", errString("用法:/role-pack export <标识> [路径] 或 /role-pack import <包路径> [--as 名] [--overwrite]")
	}
	switch strings.TrimSpace(args[0]) {
	case "export":
		return h.cmdRolePackExport(args[1:])
	case "import":
		return h.cmdRolePackImport(args[1:])
	default:
		return "", errString("子命令只能是 export 或 import(得到:" + args[0] + ")")
	}
}
