// Package rolepack:角色包(单文件导出/导入)—— 把一个角色整体打成一个 zip 分享出去,
// 在别处一键导入。
//
//	gah-role.json                           格式标识 + 版本 + 原始 ID/显示名 + 导出时间/版本
//	role.yaml                               角色定义(原样搬运,不重排/不丢手写注释)
//	AGENTS.md                               工作规则(可缺)
//	skills/<名>/SKILL.md                    角色私有技能(可多个)
//
// 为什么是 zip:角色不是单个文件 —— 定义 + 规则 + 私有技能是一整棵目录,单文件分享必须能
// 无损装下它,而标准库 archive/zip 零新依赖(便携红线:单一静态二进制)。
// manifest 为什么要内容而不是只靠扩展名:接收方得知道"这是不是 gah 角色包、是哪个格式版本、
// 里面那个角色本来叫什么"。格式版本不认识就**显式拒绝**,不去猜(猜错的代价是静默丢掉字段)。
//
// 为什么单独一个包:导出/导入同时要碰两侧的私有细节 —— 角色定义走 internal/roles,
// 技能名与正文走 internal/skills,而 internal/skills 已经依赖 internal/roles(路径单一事实源),
// 反向 import 会成环。于是让本包**同时依赖两者**(DAG 不变),把"包格式"这件事收在一处。
//
// 安全口径(解包是唯一一处"文件内容来自外部"的入口,按最坏情况写):
//   - **白名单式解包**:只认上面那四种条目名,且写盘路径**由解析出的名字重新拼**(绝不
//     filepath.Join 外部路径)⇒ 路径穿越在构造上不可能;段级再禁 `.`/`..`/空段(挡住
//     `../evil`、`/abs`、`skills//x/SKILL.md`、Windows 反斜杠形态)。
//   - **上限三层**:压缩包字节数、解包后总量、条目个数;单项再各自套用与本地写入同款的上限
//     (AGENTS.md 32 KiB / SKILL.md 64 KiB)⇒ 不许"导入进来一个本地写不进去的角色"。
//   - **从不静默**:不认识的条目、重复条目、坏格式版本、未知定义键、坏技能名 —— 一律报错
//     并**不落任何盘**;写入阶段中途失败则整体回滚(删掉半成品,把备份搬回来)。
package rolepack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// ManifestName 包内清单文件名(格式标识 + 版本 + 来源信息)。
	ManifestName = "gah-role.json"
	// Format 包格式标识(manifest.format;不匹配即拒绝)。
	Format = "gah-role"
	// Version 当前包格式版本(加字段不必升版本 —— 老读方按 unknown key 拒绝;
	// 改变已有字段语义才升,并在 Import 里给老版本留迁移分支)。
	Version = 1
	// MaxPackBytes 压缩包字节上限(8 MiB;上传/读取都按它截)。
	MaxPackBytes = 8 << 20
	// MaxTotalBytes 解包后总量上限(32 MiB)。角色本就该很小:32 KiB 规则 + 每个技能 64 KiB。
	MaxTotalBytes = 32 << 20
	// maxEntries 条目个数上限(技能数与目录条目都算进去)。
	maxEntries = 512
	// maxManifestBytes 清单字节上限(防"用清单吃满总量")。
	maxManifestBytes = 64 << 10
)

// Manifest 包内清单。接收方先读它:格式对不对、哪个版本、原本是哪个角色。
type Manifest struct {
	Format     string `json:"format"`                // 恒为 Format
	Version    int    `json:"version"`               // 包格式版本
	ID         string `json:"id"`                    // 导出时角色的 ID(导入目标默认取它)
	Name       string `json:"name,omitempty"`        // 导出时显示名(仅供人看/回显)
	ExportedAt string `json:"exported_at"`           // RFC3339
	GAHVersion string `json:"gah_version,omitempty"` // 导出方 gah 版本(诊断用,不参与判定)
}

// FileName 导出建议文件名(命令默认落点与 Web 下载头共用一处命名:两处各写一份必然漂)。
func FileName(id string) string { return "gah-role-" + id + ".zip" }

// Export 把角色打包成 zip 字节(角色不存在/坏定义 → 报错,不产出半截包)。
func Export(id string) ([]byte, error) { return exportAt(id, time.Now()) }

// exportAt Export 的可注入时钟版本(测试要确定性字节:同一份角色 + 同一时刻 ⇒ 同一份包)。
func exportAt(id string, now time.Time) ([]byte, error) {
	spec, err := roles.Store{}.Get(id)
	if err != nil {
		return nil, err
	}
	// role.yaml **原样搬运**:手写过注释/键顺序的 role.yaml 经"解析再序列化"会被重排
	// (甚至丢注释)—— 导出不是修改,别替用户重写文件。
	def, err := os.ReadFile(filepath.Join(roles.Dir(id), roles.FileName))
	if err != nil {
		return nil, fmt.Errorf("读取角色定义失败: %w", err)
	}
	man, err := json.MarshalIndent(Manifest{
		Format: Format, Version: Version, ID: spec.ID, Name: spec.Name,
		ExportedAt: now.UTC().Format(time.RFC3339), GAHVersion: gahVersion(),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("清单序列化失败: %w", err)
	}
	man = append(man, '\n')

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// 固定写入顺序(清单 → 定义 → 规则 → 技能按名字典序):同一份角色导两次是同一串字节,
	// 便于比对 diff 与校验(与 extplugins 的"确定性产物"同一条纪律)。
	entries := []packEntry{{ManifestName, man}, {roles.FileName, def}}
	if strings.TrimSpace(spec.AGENTS) != "" || spec.AGENTSBytes > 0 {
		entries = append(entries, packEntry{roles.AgentsName, []byte(spec.AGENTS)})
	}
	names := append([]string(nil), spec.OwnSkills...)
	sort.Strings(names)
	for _, name := range names {
		body, err := skills.ForRole(id).Read(name)
		if err != nil {
			return nil, fmt.Errorf("读取私有技能 %s 失败: %w", name, err)
		}
		entries = append(entries, packEntry{skillEntry(name), []byte(body)})
	}
	for _, e := range entries {
		// Modified 统一取导出时刻(不用各文件 mtime:包内容才是有意义的,打包机的时钟噪声不是)。
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: now}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, fmt.Errorf("打包失败(%s): %w", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, fmt.Errorf("打包失败(%s): %w", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("打包失败: %w", err)
	}
	return buf.Bytes(), nil
}

type packEntry struct {
	name string
	data []byte
}

// ImportOptions 导入选项。
type ImportOptions struct {
	// As 目标 ID(空 = 用包里的原始 ID)。给"同一个分享角色已经在本地存在、想并存一份"用。
	As string
	// Overwrite 目标已存在时是否覆盖。缺省 **false = 显式拒绝**(不静默覆盖别人的角色);
	// 置真时旧份先整份搬进 roles/.trash(可恢复),当前角色仍拒绝覆盖。
	Overwrite bool
}

// ImportResult 导入结果(回执要能回答"落在哪、带了什么、旧的那份去哪了")。
type ImportResult struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	AgentsLen  int      `json:"agents_bytes"`
	Skills     []string `json:"skills"`
	Replaced   bool     `json:"replaced"`              // 是否覆盖了已有角色
	BackupName string   `json:"backup_name,omitempty"` // 被覆盖那份在回收站里的条目名
	Manifest   Manifest `json:"manifest"`
}

// Inspect 只读一个包的清单(不落盘、不校验内容)—— 用于"导入前先看清这是什么"与
// "覆盖同名文件前先确认那也是个角色包"(不拿别人的文件当自己的草稿纸)。
func Inspect(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) == 0 || len(data) > MaxPackBytes {
		return m, errors.New("不是有效的角色包(大小异常)")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return m, fmt.Errorf("不是有效的角色包(zip 解析失败): %w", err)
	}
	for _, f := range zr.File {
		if f.Name != ManifestName {
			continue
		}
		raw, err := readEntry(f, maxManifestBytes, new(int))
		if err != nil {
			return m, err
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return m, fmt.Errorf("角色包清单解析失败: %w", err)
		}
		if m.Format != Format {
			return m, fmt.Errorf("不是 gah 角色包(格式标识 = %q)", m.Format)
		}
		if m.Version != Version {
			return m, fmt.Errorf("角色包格式版本不支持:%d(本版本只认 %d)", m.Version, Version)
		}
		return m, nil
	}
	return m, fmt.Errorf("角色包缺 %s", ManifestName)
}

// Import 导入一个角色包。成功返回落点信息;任何校验/写入失败都**不留半成品**。
func Import(data []byte, opts ImportOptions) (ImportResult, error) {
	if len(data) == 0 {
		return ImportResult{}, errors.New("角色包是空的")
	}
	if len(data) > MaxPackBytes {
		return ImportResult{}, fmt.Errorf("角色包超上限:%d 字节 > %d 字节", len(data), MaxPackBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ImportResult{}, fmt.Errorf("不是有效的角色包(zip 解析失败): %w", err)
	}
	st, err := readPack(zr)
	if err != nil {
		return ImportResult{}, err
	}
	// —— 目标 ID ——
	id := opts.As
	if id == "" {
		id = strings.TrimSpace(st.manifest.ID)
		if id == "" {
			return ImportResult{}, errors.New("角色包的清单里没有 id(请用 --as/「导入为」指定目标 ID)")
		}
	}
	if err := roles.ValidateID(id); err != nil {
		return ImportResult{}, err
	}
	// —— 定义 ——
	// strictKeys=true:包里出现本版本不认识的键 ⇒ 拒绝,不静默丢字段(见 ParseDefinition 注释)。
	spec, err := roles.ParseDefinition(id, st.def, true)
	if err != nil {
		return ImportResult{}, err
	}
	// —— 规则正文与技能:先全量校验,再动盘 ——
	if len(st.agents) > roles.MaxAgentsBytes {
		return ImportResult{}, fmt.Errorf("角色包里的 %s 超上限:%d 字节 > %d 字节(本机写入同样会拒)",
			roles.AgentsName, len(st.agents), roles.MaxAgentsBytes)
	}
	for _, sk := range st.skills {
		if err := skills.ValidateName(sk.name); err != nil {
			return ImportResult{}, fmt.Errorf("角色包含非法技能名: %w", err)
		}
		if len(sk.body) > skills.MaxBytes {
			return ImportResult{}, fmt.Errorf("角色包里的技能 %s 超上限:%d 字节 > %d 字节", sk.name, len(sk.body), skills.MaxBytes)
		}
		// frontmatter 的 name 必须与目录名一致(与 Library.Write 同一条不变量):
		// 不一致会让"清单里的名字"与"目录名"分成两个,用户看到两个名字。
		if fn := skills.ParseName(string(sk.body)); fn != "" && fn != sk.name {
			return ImportResult{}, fmt.Errorf("角色包里技能 %s 的 frontmatter name 是 %s,与目录名不一致", sk.name, fn)
		}
	}
	store := roles.Store{}
	res := ImportResult{ID: id, Name: spec.Name, AgentsLen: len(st.agents), Manifest: st.manifest}
	res.Skills = skillNames(st.skills)
	if res.Skills == nil {
		res.Skills = []string{}
	}
	// 目标位置若被一个**同名文件**(或软链)占着:显式拒绝 —— Exist 只认目录(判否),
	// 直接往下走会先写失败、再由回滚 RemoveAll **把用户那个文件删掉**(回滚不该销毁外物)。
	if fi, err := os.Lstat(roles.Dir(id)); err == nil && !fi.IsDir() {
		return ImportResult{}, fmt.Errorf("角色目录位置被一个同名文件占着:%s(请先清理后再导入)", roles.Dir(id))
	}
	// —— 覆盖:旧份先搬进回收站(可逆) ——
	existed := store.Exist(id)
	if existed {
		if !opts.Overwrite {
			return ImportResult{}, fmt.Errorf("角色 %s 已存在:要覆盖请在命令里加 force(面板/接口用 overwrite),或改用「导入为」另一个 ID", id)
		}
		backup, err := store.MoveToTrash(id) // 当前角色在这里被拒
		if err != nil {
			return ImportResult{}, err
		}
		res.Replaced, res.BackupName = true, backup
	}
	// —— 落盘(失败整体回滚) ——
	if err := writeRole(id, spec, st.agents, st.skills); err != nil {
		return ImportResult{}, rollback(store, id, res.BackupName, err)
	}
	return res, nil
}

// writeRole 定义 → 规则 → 技能(每一步都是既有的原子写与校验入口,不另起一套)。
// 参数名 ownSkills 而不是 skills:包名 skills 在文里要用,同名参数会把它遮住。
func writeRole(id string, spec sdk.RoleSpec, agents []byte, ownSkills []roleSkill) error {
	spec.ID = id
	store := roles.Store{} // 复合字面量不能直接出现在 if 的初始化语句里(Go 语法限制)
	if err := store.Save(spec); err != nil {
		return err
	}
	if len(agents) > 0 {
		if err := store.SetAgents(id, string(agents)); err != nil {
			return err
		}
	}
	for _, sk := range ownSkills {
		// 走 Library.Write:技能名/frontmatter 一致性/上限/原子写全在同一处,导入不是第二套实现。
		if err := skills.ForRole(id).Write(sk.name, string(sk.body), false); err != nil {
			return err
		}
	}
	return nil
}

// rollback 半成品清掉,并把被覆盖的那份从回收站搬回原位(搬不回来就如实说 —— 不许谎称"已回滚")。
func rollback(store roles.Store, id, backup string, cause error) error {
	rmErr := sdk.RemoveTree(roles.Dir(id))
	if backup == "" {
		if rmErr != nil {
			return fmt.Errorf("导入失败: %v;且清理半成品目录也失败: %w(请手工删除 %s)", cause, rmErr, roles.Dir(id))
		}
		// 没有旧份要保:直接如实转述底层错误,但要说清这是导入失败(用户看到的入口是"导入")
		return fmt.Errorf("导入失败(未落盘): %w", cause)
	}
	if rmErr != nil {
		return fmt.Errorf("导入失败: %v;清理半成品失败: %w(旧份仍在回收站 %s/%s,可手工恢复)", cause, rmErr, roles.TrashDir(), backup)
	}
	if _, err := store.Restore(backup); err != nil {
		return fmt.Errorf("导入失败: %v;回滚也失败:%w(旧份仍在回收站 %s/%s,可用 /api/trash/restore 或手工恢复)",
			cause, err, roles.TrashDir(), backup)
	}
	return fmt.Errorf("导入失败(已回滚,原角色未受影响): %w", cause)
}

// roleSkill 包里一个技能(名字 + 正文)。
type roleSkill struct {
	name string
	body []byte
}

// packState 已解析并校验过"结构"的角色包(内容层校验在 Import 里做,以便先算清目标 ID)。
type packState struct {
	manifest Manifest
	def      []byte
	agents   []byte
	skills   []roleSkill
}

// readPack 白名单式读取包内容(结构/格式版本/上限/重复/路径形态)。
func readPack(zr *zip.Reader) (packState, error) {
	var st packState
	if len(zr.File) > maxEntries {
		return st, fmt.Errorf("角色包条目过多:%d > %d", len(zr.File), maxEntries)
	}
	seen := make(map[string]bool, len(zr.File))
	total := 0
	for _, f := range zr.File {
		name := f.Name
		if err := checkEntryPath(name); err != nil {
			return st, err
		}
		if seen[name] {
			return st, fmt.Errorf("角色包含重复条目:%s", name)
		}
		seen[name] = true
		switch {
		case name == ManifestName:
			raw, err := readEntry(f, maxManifestBytes, &total)
			if err != nil {
				return st, err
			}
			if err := json.Unmarshal(raw, &st.manifest); err != nil {
				return st, fmt.Errorf("角色包清单解析失败: %w", err)
			}
		case name == roles.FileName:
			raw, err := readEntry(f, roles.MaxAgentsBytes*8, &total) // 定义本身很小;给足余量但仍有界
			if err != nil {
				return st, err
			}
			st.def = raw
		case name == roles.AgentsName:
			raw, err := readEntry(f, roles.MaxAgentsBytes+1, &total) // +1:超限也要读出来才能报"超了多少"
			if err != nil {
				return st, err
			}
			st.agents = raw
		default:
			skill, ok := parseSkillEntry(name)
			if !ok {
				return st, fmt.Errorf("角色包含不认识的条目:%s(只接受 %s / %s / %s / skills/<名>/SKILL.md)",
					name, ManifestName, roles.FileName, roles.AgentsName)
			}
			raw, err := readEntry(f, skills.MaxBytes+1, &total)
			if err != nil {
				return st, err
			}
			st.skills = append(st.skills, roleSkill{name: skill, body: raw})
		}
	}
	if st.manifest.Format != Format {
		return st, fmt.Errorf("不是 gah 角色包(格式标识 = %q,期望 %q)", st.manifest.Format, Format)
	}
	if st.manifest.Version != Version {
		return st, fmt.Errorf("角色包格式版本不支持:%d(本版本只认 %d;请升级 gah 或换用兼容的包)",
			st.manifest.Version, Version)
	}
	if st.def == nil {
		return st, fmt.Errorf("角色包缺 %s", roles.FileName)
	}
	sort.Slice(st.skills, func(i, j int) bool { return st.skills[i].name < st.skills[j].name })
	return st, nil
}

// readEntry 读一个条目(三层上限里的"单项上限"与"总量"都在这里收口)。
func readEntry(f *zip.File, limit int, total *int) ([]byte, error) {
	// zip 头是外部输入、可以撒谎:声明的解压大小只用来早退(省 I/O),真正的上限按**实际读到的字节数**算。
	if f.UncompressedSize64 > uint64(MaxTotalBytes) {
		return nil, fmt.Errorf("角色包条目 %s 声明大小异常:%d 字节", f.Name, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("角色包条目 %s 打开失败: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := readLimit(rc, limit)
	if err != nil {
		return nil, fmt.Errorf("角色包条目 %s: %w", f.Name, err)
	}
	*total += len(raw)
	if *total > MaxTotalBytes {
		return nil, fmt.Errorf("角色包解包后超上限:%d 字节 > %d 字节", *total, MaxTotalBytes)
	}
	return raw, nil
}

// readLimit 读至多 limit 字节(读满 limit+1 即判定超限:既知道超了,又不会把内容读进内存)。
func readLimit(r io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = MaxTotalBytes
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("内容超上限(> %d 字节)", limit)
	}
	return raw, nil
}

// checkEntryPath 段级路径校验(白名单之外的第二道闸,专治"看着合法其实穿越"的形态)。
func checkEntryPath(name string) error {
	if name == "" {
		return errors.New("角色包含空条目名")
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("角色包含非法条目名(含反斜杠):%s", name)
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("角色包含绝对路径条目(已拒绝):%s", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("角色包含路径穿越条目(已拒绝):%s", name)
		}
	}
	return nil
}

// parseSkillEntry 严格解析 skills/<名>/SKILL.md(嵌套目录/别的文件名一律不认)。
func parseSkillEntry(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, roles.SkillsDirName+"/")
	if !ok {
		return "", false
	}
	seg, ok := strings.CutSuffix(rest, "/"+skills.FileName)
	if !ok || seg == "" || strings.Contains(seg, "/") {
		return "", false
	}
	return seg, true
}

// skillEntry 包内技能条目名(与 parseSkillEntry 必须成对)。
func skillEntry(name string) string { return roles.SkillsDirName + "/" + name + "/" + skills.FileName }

// skillNames 技能名清单(供回执;顺序已按名字典序)。
func skillNames(list []roleSkill) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, sk := range list {
		out = append(out, sk.name)
	}
	return out
}

// gahVersion 导出方版本(诊断用;boot 由 main.version 注入 —— 未注入时留空,不编一个假版本)。
func gahVersion() string { return os.Getenv("GAH_VERSION") }
