// 预算与 zip 炸弹守卫单测。
package hostdocview

import (
	"errors"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestBudgetDefaults(t *testing.T) {
	d := DefaultBudget()
	if d.MaxInputBytes != 50<<20 || d.MaxPreviewBytes != 1<<20 || d.MaxBlocks != 4000 {
		t.Fatalf("默认预算偏离: %+v", d)
	}
	if d.Timeout != 10*time.Second {
		t.Fatalf("默认超时偏离: %v", d.Timeout)
	}
	// 零值补齐
	z := Budget{}.withDefaults()
	if z != d {
		t.Fatalf("零值应补为默认: %+v", z)
	}
	// 部分覆盖:只改一个维度,其余保持默认
	p := Budget{MaxInputBytes: 1024}.withDefaults()
	if p.MaxInputBytes != 1024 || p.MaxPreviewBytes != d.MaxPreviewBytes {
		t.Fatalf("部分覆盖失败: %+v", p)
	}
}

func TestTruncText(t *testing.T) {
	// 按字符(非字节)截断:CJK 不得被切断
	got, cut := truncText("中文abc", 2)
	if got != "中文" || !cut {
		t.Fatalf("truncText = (%q,%v)", got, cut)
	}
	if got, cut := truncText("short", 10); cut || got != "short" {
		t.Fatalf("未超限不应截断: (%q,%v)", got, cut)
	}
}

func TestZipBudgetBomb(t *testing.T) {
	b := Budget{
		MaxZipPartBytes: 100,
		MaxZipTotal:     250,
		MaxZipRatio:     10,
	}.withDefaults()
	// withDefaults 会把 <0 补默认,但显式小值保留
	zb := newZipBudget(b)

	if err := zb.check("a", 50, 10); err != nil {
		t.Fatalf("正常条目不应报错: %v", err)
	}
	// 单 part 超限
	if err := zb.check("big", 101, 10); !errors.Is(err, sdk.ErrDocTooLarge) {
		t.Fatalf("单 part 超限应报 ErrDocTooLarge,得 %v", err)
	}
	// 膨胀比炸弹(>1MiB 且比值超限)
	if err := zb.check("bomb", 5<<20, 1024); !errors.Is(err, sdk.ErrDocTooLarge) {
		t.Fatalf("膨胀比应报 ErrDocTooLarge,得 %v", err)
	}
	// 累计超限(声明预扣:50 已计入,再叠 250 → 300 > 250)
	if err := zb.check("c", 250, 100); !errors.Is(err, sdk.ErrDocTooLarge) {
		t.Fatalf("累计超限应报 ErrDocTooLarge,得 %v", err)
	}
}

func TestZipBudgetCumulativeDeclared(t *testing.T) {
	zb := newZipBudget(Budget{MaxZipTotal: 150, MaxZipPartBytes: 1000, MaxZipRatio: 1 << 40}.withDefaults())
	if err := zb.check("a", 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := zb.check("b", 100, 50); !errors.Is(err, sdk.ErrDocTooLarge) {
		t.Fatalf("累计声明超限应报错,得 %v", err)
	}
}
