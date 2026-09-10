// OOXML 通用层(D 组 D2/D3):zip 容器 + 内容类型 + 关系图(rels)+ 预算受控部件读取。
//
// 纪律(对齐 DOC_PREVIEW_PLAN §5.3):
//   - stdlib archive/zip + encoding/xml,零第三方依赖,无 CGO;
//   - 大部件不整体 Unmarshal:经 xml.Decoder 逐 token 流式解析(调用方负责);
//   - 每个部件读取都过 zip 预算(单 part / 累计 / 膨胀比三重封顶),不信任声明尺寸;
//   - 关系图惰性解析并缓存(图片/超链接目标解析)。
package hostdocview

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器(容器内图片尺寸探测)
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"io"
	"path"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ooxmlRel 一条 OPC 关系(Id → 类型 + 目标)。
type ooxmlRel struct {
	ID     string
	Type   string
	Target string // 已归一为容器内绝对路径(/word/media/image1.png 形态,无前导斜杠)
}

// ooxml OOXML 容器读取器。
type ooxml struct {
	abs       string
	zr        *zip.ReadCloser
	zb        *zipBudget
	files     map[string]*zip.File
	relsCache map[string]map[string]ooxmlRel
	warnings  []string
}

// openOOXML 打开 OOXML 容器(校验 zip 爆炸预算)。
func openOOXML(abs string, b Budget) (*ooxml, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开 OOXML 容器失败: %v", sdk.ErrDocParse, err)
	}
	o := &ooxml{abs: abs, zr: zr, zb: newZipBudget(b), files: map[string]*zip.File{}, relsCache: map[string]map[string]ooxmlRel{}}
	for _, f := range zr.File {
		o.files[strings.TrimPrefix(f.Name, "/")] = f
	}
	// 容器级预检:总解压量声明超限直接拒绝(防"很多大部件"绕过单 part 检查)
	if err := o.precheck(); err != nil {
		_ = zr.Close()
		return nil, err
	}
	return o, nil
}

// precheck 全容器声明尺寸预检(单 part / 单 part 膨胀比;累计由 read 路径按需扣减)。
func (o *ooxml) precheck() error {
	for name, f := range o.files {
		if f.FileInfo().IsDir() {
			continue
		}
		if err := o.zb.check(name, int64(f.UncompressedSize64), int64(f.CompressedSize64)); err != nil {
			return err
		}
	}
	return nil
}

func (o *ooxml) Close() error { return o.zr.Close() }

// has 部件是否存在(SS 预检用)。
func (o *ooxml) has(name string) bool {
	_, ok := o.files[strings.TrimPrefix(name, "/")]
	return ok
}

// hasPrefix 是否存在某前缀的部件(如 word/header)。
func (o *ooxml) hasPrefix(prefix string) bool {
	for n := range o.files {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// reader 打开部件读取流(预算已由 precheck 校验;读取按真实字节数再封顶)。
func (o *ooxml) reader(name string) (io.ReadCloser, error) {
	f, ok := o.files[strings.TrimPrefix(name, "/")]
	if !ok {
		return nil, fmt.Errorf("%w: 容器缺少部件 %s", sdk.ErrDocParse, name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: 读取部件 %s 失败: %v", sdk.ErrDocParse, name, err)
	}
	limit := int64(f.UncompressedSize64) + 1
	return io.NopCloser(io.LimitReader(rc, limit)), nil
}

// contentTypes 解析 [Content_Types].xml → 扩展名/部件 → 内容类型。
func (o *ooxml) contentTypes() (byExt, byPart map[string]string) {
	byExt, byPart = map[string]string{}, map[string]string{}
	rc, err := o.reader("[Content_Types].xml")
	if err != nil {
		return byExt, byPart
	}
	defer rc.Close()
	var ct struct {
		Defaults []struct {
			Extension   string `xml:"Extension,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Default"`
		Overrides []struct {
			PartName    string `xml:"PartName,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Override"`
	}
	if err := xml.NewDecoder(rc).Decode(&ct); err != nil {
		return byExt, byPart
	}
	for _, d := range ct.Defaults {
		byExt[strings.ToLower(d.Extension)] = d.ContentType
	}
	for _, ov := range ct.Overrides {
		byPart[strings.TrimPrefix(ov.PartName, "/")] = ov.ContentType
	}
	return byExt, byPart
}

// partType 部件内容类型(Override 优先,其次按扩展名 Default)。
func (o *ooxml) partType(name string) string {
	byExt, byPart := o.contentTypes()
	name = strings.TrimPrefix(name, "/")
	if t, ok := byPart[name]; ok {
		return t
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	return byExt[ext]
}

// mainPart 依内容类型找主部件(docx: word/document.xml;xlsx: xl/workbook.xml;pptx: ppt/presentation.xml)。
// 先查 _rels/.rels 的 officeDocument 关系,失败回退固定候选。
func (o *ooxml) mainPart(kind string) string {
	for _, rel := range o.rels("") {
		if strings.HasSuffix(rel.Type, "/officeDocument") {
			return rel.Target
		}
	}
	switch kind {
	case "docx":
		return "word/document.xml"
	case "xlsx":
		return "xl/workbook.xml"
	case "pptx":
		return "ppt/presentation.xml"
	}
	return ""
}

// rels 解析某部件的关系表(part 为空 = 包根 _rels/.rels)。
func (o *ooxml) rels(part string) map[string]ooxmlRel {
	part = strings.TrimPrefix(part, "/")
	if cached, ok := o.relsCache[part]; ok {
		return cached
	}
	base := path.Dir(part)
	if base == "." {
		base = ""
	}
	relsName := path.Join(base, "_rels", path.Base(part)+".rels")
	if part == "" {
		relsName = "_rels/.rels"
	}
	out := map[string]ooxmlRel{}
	rc, err := o.reader(relsName)
	if err != nil {
		o.relsCache[part] = out
		return out
	}
	defer rc.Close()
	var rl struct {
		Rels []struct {
			ID         string `xml:"Id,attr"`
			Type       string `xml:"Type,attr"`
			Target     string `xml:"Target,attr"`
			TargetMode string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.NewDecoder(rc).Decode(&rl); err != nil {
		o.warnings = append(o.warnings, fmt.Sprintf("关系表 %s 解析失败: %v", relsName, err))
		o.relsCache[part] = out
		return out
	}
	for _, r := range rl.Rels {
		target := r.Target
		if r.TargetMode == "External" || strings.Contains(target, "://") {
			out[r.ID] = ooxmlRel{ID: r.ID, Type: r.Type, Target: target}
			continue
		}
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join(base, target)
		}
		out[r.ID] = ooxmlRel{ID: r.ID, Type: r.Type, Target: path.Clean(target)}
	}
	o.relsCache[part] = out
	return out
}

// imageDims 读取容器内图片部件并探测尺寸(PNG/JPEG/GIF;失败不致命)。
func (o *ooxml) imageDims(part string) (int, int, error) {
	rc, err := o.reader(part)
	if err != nil {
		return 0, 0, err
	}
	defer rc.Close()
	cfg, _, err := image.DecodeConfig(rc)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// addWarning 汇总容器解析警告(去重 + 上限)。
func (o *ooxml) addWarning(msg string) {
	for _, w := range o.warnings {
		if w == msg {
			return
		}
	}
	if len(o.warnings) >= 50 {
		return
	}
	o.warnings = append(o.warnings, msg)
}
