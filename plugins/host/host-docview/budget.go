// 预算与红线守卫(对齐 DOC_PREVIEW_PLAN §8):源大小 / 预览 / 表格 / 资产 / zip 解压炸弹 / 抽取超时。
// 全部可配(插件 data),超限一律写 Truncated 或 Warnings —— 绝不静默截断。
package hostdocview

import (
	"fmt"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Budget 文档预览预算(零值请用 DefaultBudget)。
type Budget struct {
	MaxInputBytes   int64         // 源文件大小上限(默认 50MiB)
	MaxPreviewBytes int64         // 面板预览文本预算(默认 1MiB)
	MaxBlocks       int           // 块数上限(默认 4000)
	MaxTableRows    int           // 表格行上限(默认 200)
	MaxTableCols    int           // 表格列上限(默认 60)
	MaxCellChars    int           // 单元格字符上限(默认 200)
	MaxAssets       int           // 内嵌资产数上限(默认 20)
	MaxAssetsBytes  int64         // 内嵌资产合计字节上限(默认 20MiB)
	MaxZipPartBytes int64         // 单 zip part 上限(默认 50MiB)
	MaxZipTotal     int64         // zip 解压累计上限(默认 200MiB)
	MaxZipRatio     int64         // 解压膨胀比上限(默认 1000×)
	MaxTextLines    int           // Text 默认行数(默认 2000)
	MaxLineChars    int           // Text 单行字符上限(默认 2000)
	Timeout         time.Duration // 抽取超时(默认 10s)
}

// DefaultBudget 默认预算(对齐 dsh-document 默认集,便于用户迁移直觉)。
func DefaultBudget() Budget {
	return Budget{
		MaxInputBytes:   50 << 20,
		MaxPreviewBytes: 1 << 20,
		MaxBlocks:       4000,
		MaxTableRows:    200,
		MaxTableCols:    60,
		MaxCellChars:    200,
		MaxAssets:       20,
		MaxAssetsBytes:  20 << 20,
		MaxZipPartBytes: 50 << 20,
		MaxZipTotal:     200 << 20,
		MaxZipRatio:     1000,
		MaxTextLines:    2000,
		MaxLineChars:    2000,
		Timeout:         10 * time.Second,
	}
}

// withDefaults 把零值字段补为默认(允许插件 data 只覆盖部分维度)。
func (b Budget) withDefaults() Budget {
	d := DefaultBudget()
	set := func(v *int64, def int64) {
		if *v <= 0 {
			*v = def
		}
	}
	set(&b.MaxInputBytes, d.MaxInputBytes)
	set(&b.MaxPreviewBytes, d.MaxPreviewBytes)
	set(&b.MaxAssetsBytes, d.MaxAssetsBytes)
	set(&b.MaxZipPartBytes, d.MaxZipPartBytes)
	set(&b.MaxZipTotal, d.MaxZipTotal)
	set(&b.MaxZipRatio, d.MaxZipRatio)
	for _, p := range []struct {
		v   *int
		def int
	}{
		{&b.MaxBlocks, d.MaxBlocks}, {&b.MaxTableRows, d.MaxTableRows}, {&b.MaxTableCols, d.MaxTableCols},
		{&b.MaxCellChars, d.MaxCellChars}, {&b.MaxAssets, d.MaxAssets},
		{&b.MaxTextLines, d.MaxTextLines}, {&b.MaxLineChars, d.MaxLineChars},
	} {
		if *p.v <= 0 {
			*p.v = p.def
		}
	}
	if b.Timeout <= 0 {
		b.Timeout = d.Timeout
	}
	return b
}

// truncText 按字符上限截断,返回截断后的文本与是否发生截断。
func truncText(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]), true
}

// zipBudget zip 解压预算累计器(炸弹防护:单 part / 累计 / 膨胀比三重封顶)。
type zipBudget struct {
	b     Budget
	total int64 // 已登记(预扣)的解压后字节合计
}

func newZipBudget(b Budget) *zipBudget { return &zipBudget{b: b} }

// check 解压前校验单个 part 的声明尺寸与压缩比,并按声明尺寸预扣累计额度
// (声明值不可信:实际读取仍应经 io.LimitReader(声明+1) 封顶)。
// compressed <= 0 表示无压缩信息(存储式条目),跳过比例检查。
func (z *zipBudget) check(name string, uncompressed, compressed int64) error {
	if uncompressed > z.b.MaxZipPartBytes {
		return fmt.Errorf("%w: zip 条目 %s 解压后 %d 字节超出单 part 上限 %d", sdk.ErrDocTooLarge, name, uncompressed, z.b.MaxZipPartBytes)
	}
	if compressed > 0 && uncompressed > 1<<20 {
		if ratio := uncompressed / compressed; ratio > z.b.MaxZipRatio {
			return fmt.Errorf("%w: zip 条目 %s 膨胀比 %d× 超出上限 %d×(疑似解压炸弹)", sdk.ErrDocTooLarge, name, ratio, z.b.MaxZipRatio)
		}
	}
	if z.total+uncompressed > z.b.MaxZipTotal {
		return fmt.Errorf("%w: zip 解压累计 %d 字节超出上限 %d(疑似解压炸弹)", sdk.ErrDocTooLarge, z.total+uncompressed, z.b.MaxZipTotal)
	}
	z.total += uncompressed
	return nil
}
