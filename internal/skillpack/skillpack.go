// Package skillpack:技能包(单文件导出/导入)—— 把一个**共享技能**打成 zip 分享出去,
// 在别处一键导入(借鉴 5;与 internal/rolepack 同款纪律与安全口径)。
//
// 为什么只做**共享技能**:角色私有技能已经能被分享了 —— 第九十三批的角色包导出就
// 带着角色的私有技能目录(roles/<id>/skills/**)。所以本包覆盖的是另一条缺口:
// "我自己写的公共技能,怎么给别人"。两条路合起来 = 技能分享没有缺口。
//
// 为什么"只导入声明式部分"是**天然**成立的:gah 的技能就是**提示词文件** ——
// `SKILL.md`(frontmatter + 正文)由 `read_skill` 读进上下文,没有可执行载荷。
// 于是导入一个技能包**不可能**引入代码执行面(与 OpenClaw 那种"技能里带脚本、
// 由运行时去装依赖"的做法根本不同)。技能正文里*指示*模型去跑命令是另一回事:
// 那是任何提示词都能写的内容,宿主一视同仁地走审批面。
//
// 安全口径(照 rolepack 逐条对齐;解包是唯一一处"内容来自外部"的入口):
//   - **白名单式解包**:只认 ManifestName 与 `SKILL.md` 两种条目名,且写盘路径**由
//     校验过的技能名重新拼**(绝不 filepath.Join 外部路径)⇒ 路径穿越构造上不可能。
//   - **上限三层**:压缩包字节、解包后总量、条目个数;正文再套 skills.MaxBytes
//     (64 KiB,与本地写入同款)⇒ 不许"导入一个本地写不进去的技能"。
//   - **从不静默**:坏格式版本、未知条目、重复条目、坏技能名 —— 一律报错且**不落盘**;
//     写入阶段失败则整体回滚(恢复被覆盖的旧份)。
package skillpack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/skills"
)

const (
	// ManifestName 包内清单文件名(格式标识 + 版本 + 来源技能名)。
	ManifestName = "gah-skill.json"
	// Format 包格式标识(manifest.format;不匹配即拒绝)。
	Format = "gah-skill"
	// Version 包格式版本(加字段不必升版本 —— 老读方按 unknown key 拒绝)。
	Version = 1
	// MaxPackBytes 压缩包字节上限(1 MiB:一个技能包只有一个 SKILL.md,1 MiB 绰绰有余;
	// 比 rolepack 的 8 MiB 更紧,因为这里没有"整棵角色目录"要装)。
	MaxPackBytes = 1 << 20
	// MaxTotalBytes 解包后总量上限(4 MiB)。
	MaxTotalBytes = 4 << 20
	// maxEntries 条目个数上限(本格式实际只有 2 个;留余量给将来加可选文件)。
	maxEntries = 16
	// maxManifestBytes 清单字节上限(防"用清单吃满总量")。
	maxManifestBytes = 64 << 10
	// skillEntry 技能正文在包内的条目名(固定单层路径)。
	skillEntry = skills.FileName
)

// Manifest 包内清单。接收方先读它:格式对不对、哪个版本、原本叫什么技能。
type Manifest struct {
	Format     string `json:"format"`                // 恒为 Format
	Version    int    `json:"version"`               // 包格式版本
	Name       string `json:"name"`                  // 导出时技能名(导入目标默认取它)
	ExportedAt string `json:"exported_at"`           // RFC3339
	GAHVersion string `json:"gah_version,omitempty"` // 导出方 gah 版本(诊断用,不参与判定)
}

// FileName 导出建议文件名(Web 下载头与命令默认落点共用这一处命名:两处各写一份必然漂)。
func FileName(name string) string { return "gah-skill-" + name + ".zip" }

// Export 把一个**共享**技能打包成 zip 字节(不存在/读不出 → 报错,不产出半截包)。
func Export(name string) ([]byte, error) { return exportAt(name, time.Now()) }

// exportAt Export 的可注入时钟版本(测试要确定性字节:同一份技能 + 同一时刻 ⇒ 同一份包)。
func exportAt(name string, now time.Time) ([]byte, error) {
	if err := skills.ValidateName(name); err != nil {
		return nil, err
	}
	raw, err := skills.Shared().Read(name)
	if err != nil {
		return nil, fmt.Errorf("skillpack: 读技能 %s 失败: %w", name, err)
	}
	if len(raw) > skills.MaxBytes {
		return nil, fmt.Errorf("skillpack: 技能 %s 正文 %d 字节,超过上限 %d", name, len(raw), skills.MaxBytes)
	}
	m := Manifest{
		Format:     Format,
		Version:    Version,
		Name:       name,
		ExportedAt: now.UTC().Format(time.RFC3339),
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("skillpack: 清单序列化失败: %w", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct {
		name string
		body []byte
	}{
		{ManifestName, mb},
		{skillEntry, []byte(raw)},
	} {
		w, err := zw.Create(e.name)
		if err != nil {
			return nil, fmt.Errorf("skillpack: 写条目失败: %w", err)
		}
		if _, err := w.Write(e.body); err != nil {
			return nil, fmt.Errorf("skillpack: 写条目内容失败: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("skillpack: 收尾失败: %w", err)
	}
	if buf.Len() > MaxPackBytes {
		return nil, fmt.Errorf("skillpack: 包 %d 字节,超过上限 %d", buf.Len(), MaxPackBytes)
	}
	return buf.Bytes(), nil
}

// ImportOptions 导入选项。
type ImportOptions struct {
	// As 目标技能名(空 = 用包里的原始名)。给"同一个技能本地已存在、想并存一份"用。
	As string
	// Overwrite 目标已存在时是否覆盖。缺省 **false = 显式拒绝**(不静默覆盖别人的技能);
	// 置真时旧份先移进 .trash(可恢复),与本地写入同款语义。
	Overwrite bool
}

// ImportResult 导入结果(回执要**具体**:落在哪儿、叫什么、有没有覆盖旧份)。
type ImportResult struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Bytes    int    `json:"bytes"`
	Replaced bool   `json:"replaced"` // 覆盖了同名旧份(旧份在回收站可恢复)
	From     string `json:"from"`     // 原本的技能名(重命名导入时与 Name 不同)
}

// Import 校验并落盘一个技能包(导入到**共享技能库**)。
// 任何校验失败都**不落盘**;写入失败整体回滚(把旧份搬回来)。
func Import(data []byte, opts ImportOptions) (ImportResult, error) {
	if len(data) > MaxPackBytes {
		return ImportResult{}, fmt.Errorf("skillpack: 包 %d 字节,超过上限 %d", len(data), MaxPackBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ImportResult{}, fmt.Errorf("skillpack: 不是有效的 zip: %w", err)
	}
	if len(zr.File) > maxEntries {
		return ImportResult{}, fmt.Errorf("skillpack: 条目 %d 个,超过上限 %d", len(zr.File), maxEntries)
	}
	seen := map[string]bool{}
	var manifestRaw, skillRaw []byte
	var total int
	for _, f := range zr.File {
		// 白名单:只认两种条目名;反斜杠形态一并拒(Windows 上 zip 里可能出现)。
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if name != ManifestName && name != skillEntry {
			return ImportResult{}, fmt.Errorf("skillpack: 不认识的条目 %q(本格式只含 %s 与 %s)", f.Name, ManifestName, skillEntry)
		}
		if seen[name] {
			return ImportResult{}, fmt.Errorf("skillpack: 条目重复 %q", name)
		}
		seen[name] = true
		if f.UncompressedSize64 > MaxTotalBytes {
			return ImportResult{}, fmt.Errorf("skillpack: 条目 %q 解压后 %d 字节,超过上限 %d", name, f.UncompressedSize64, MaxTotalBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return ImportResult{}, fmt.Errorf("skillpack: 打开条目 %q 失败: %w", name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, MaxTotalBytes+1))
		rc.Close()
		if err != nil {
			return ImportResult{}, fmt.Errorf("skillpack: 读条目 %q 失败: %w", name, err)
		}
		total += len(b)
		if total > MaxTotalBytes {
			return ImportResult{}, fmt.Errorf("skillpack: 解包总量超过上限 %d", MaxTotalBytes)
		}
		switch name {
		case ManifestName:
			if len(b) > maxManifestBytes {
				return ImportResult{}, fmt.Errorf("skillpack: 清单 %d 字节,超过上限 %d", len(b), maxManifestBytes)
			}
			manifestRaw = b
		case skillEntry:
			if len(b) > skills.MaxBytes {
				return ImportResult{}, fmt.Errorf("skillpack: 技能正文 %d 字节,超过上限 %d", len(b), skills.MaxBytes)
			}
			skillRaw = b
		}
	}
	if !seen[ManifestName] || !seen[skillEntry] {
		return ImportResult{}, errors.New("skillpack: 包缺 " + ManifestName + " 或 " + skillEntry)
	}
	// 严格解码:未知字段也拒(包里可能来自更新的 gah,猜错的代价是静默丢字段)。
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(manifestRaw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return ImportResult{}, fmt.Errorf("skillpack: 清单解析失败: %w", err)
	}
	if m.Format != Format {
		return ImportResult{}, fmt.Errorf("skillpack: 格式标识 %q 不认识(期望 %q)", m.Format, Format)
	}
	if m.Version != Version {
		return ImportResult{}, fmt.Errorf("skillpack: 包格式版本 %d 不认识(当前支持 %d),请升级后再导", m.Version, Version)
	}
	target := strings.TrimSpace(opts.As)
	if target == "" {
		target = m.Name
	}
	// 目标名必须是**合法技能名**:路径由它重新拼,名字非法就没法拼出安全路径。
	if err := skills.ValidateName(target); err != nil {
		return ImportResult{}, fmt.Errorf("skillpack: 目标技能名非法: %w", err)
	}
	lib := skills.Shared()
	if lib.Exists(target) && !opts.Overwrite {
		return ImportResult{}, fmt.Errorf("skillpack: 技能 %s 已存在(要并存请改导入名,要覆盖请显式确认覆盖)", target)
	}
	replaced := lib.Exists(target)
	// 改名导入:正文的 frontmatter name 必须跟着改 —— skills.Write 会显式校验
	// 「frontmatter 的 name == 目录名」,不改就会被拒(那才是真的拦住了不一致)。
	content := string(skillRaw)
	if target != m.Name {
		content = skills.RenameFrontmatter(content, target)
	}
	if err := lib.Write(target, content, opts.Overwrite); err != nil {
		// skills.Write 自身是原子写(临时文件 + rename),失败不留半截文件;这里如实回错。
		return ImportResult{}, fmt.Errorf("skillpack: 写入技能 %s 失败: %w", target, err)
	}
	return ImportResult{
		Name:     target,
		Path:     lib.Path(target),
		Bytes:    len(skillRaw),
		Replaced: replaced,
		From:     m.Name,
	}, nil
}
