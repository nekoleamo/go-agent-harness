// 群维度授权账本单测(G-E5-2):合并列表/排序/Stale/授权撤销/TTL 裁剪。
package im

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestBridgeGroupsMergeOrderAndAccess(t *testing.T) {
	var _ sdk.IMGroupAccessService = (*Bridge)(nil) // 契约自检
	b, _ := buildBridgeIM(t, nil)
	b.noteGroup("GROUP-1") // 已授权且有活动 → both
	b.noteGroup("GROUP-2") // 仅活动 → seen

	gs := b.Groups()
	if len(gs) != 2 {
		t.Fatalf("应有 2 个群: %+v", gs)
	}
	if gs[0].ChatID != "GROUP-1" || !gs[0].Authorized || gs[0].Source != "both" || gs[0].LastSeen.IsZero() {
		t.Fatalf("已授权群应在前且标记 both: %+v", gs[0])
	}
	if gs[1].ChatID != "GROUP-2" || gs[1].Authorized || gs[1].Source != "seen" {
		t.Fatalf("仅活动群应在后且未授权: %+v", gs[1])
	}
	if gs[0].Channel != "mock" {
		t.Fatalf("应带渠道名: %+v", gs[0])
	}
	if gs[0].Stale || gs[1].Stale {
		t.Fatalf("近期活动不应标记 stale: %+v", gs)
	}

	// 授权新群(幂等)+ 面板撤销
	if err := b.SetGroupAccess("GROUP-3", true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetGroupAccess("GROUP-3", true); err != nil {
		t.Fatalf("授权应幂等: %v", err)
	}
	found := false
	for _, e := range b.Groups() {
		if e.ChatID == "GROUP-3" && e.Authorized {
			found = true
			if !e.Stale {
				t.Fatal("无活动记录的授权群应标记 stale(提示可清理)")
			}
		}
	}
	if !found {
		t.Fatal("授权后应出现在列表")
	}
	if err := b.SetGroupAccess("GROUP-3", false); err != nil {
		t.Fatal(err)
	}
	for _, e := range b.Groups() {
		if e.ChatID == "GROUP-3" {
			t.Fatal("撤销后不应再出现在列表")
		}
	}
	// 带渠道前缀的写法也可(内部归一)
	if err := b.SetGroupAccess("mock\x00GROUP-4", true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetGroupAccess("GROUP-4", false); err != nil {
		t.Fatalf("带前缀授权后应能用裸 id 撤销: %v", err)
	}
	// 未知群撤销 / 空 id → 显式报错
	if err := b.SetGroupAccess("NOPE", false); err == nil {
		t.Fatal("撤销未知群应报错")
	}
	if err := b.SetGroupAccess("   ", true); err == nil {
		t.Fatal("空 chatID 应报错")
	}
}

func TestBridgeGroupsSeenTTLPrune(t *testing.T) {
	b, _ := buildBridgeIM(t, nil)
	b.noteGroup("FRESH")
	b.seenMu.Lock()
	b.seen = append(b.seen, seenGroup{chatID: "OLD", at: time.Now().Add(-8 * 24 * time.Hour)})
	b.seenMu.Unlock()
	for _, e := range b.Groups() {
		if e.ChatID == "OLD" {
			t.Fatal("超过 TTL 的活动记录应被裁剪")
		}
	}
	// 裁剪是写回记忆的(后续列表同样看不到)
	b.seenMu.Lock()
	n := len(b.seen)
	b.seenMu.Unlock()
	if n != 1 {
		t.Fatalf("裁剪应落回记忆,得 %d", n)
	}
	// 容量上限
	for i := 0; i < seenCap+10; i++ {
		b.noteGroup("G-" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	b.seenMu.Lock()
	n = len(b.seen)
	b.seenMu.Unlock()
	if n > seenCap {
		t.Fatalf("活动记录应受容量上限约束,得 %d", n)
	}
}

// /im list 群列表含 last-seen 与长期无活动标记(G-E5-2 呈现收口)。
func TestImListGroupLastSeen(t *testing.T) {
	b, _ := buildBridgeIM(t, nil) // AllowGroups: mock\x00GROUP-1(无活动记录)
	out := b.imCmd(context.Background(), []string{"list"})
	if !strings.Contains(out, "已授权群:") || !strings.Contains(out, "GROUP-1 [长期无活动]") {
		t.Fatalf("无活动授权群应标记长期无活动: %q", out)
	}
	b.noteGroup("GROUP-1")
	b.noteGroup("GROUP-2")
	out = b.imCmd(context.Background(), []string{"list"})
	if strings.Contains(out, "[长期无活动]") {
		t.Fatalf("有活动后不应再标记: %q", out)
	}
	if !strings.Contains(out, "GROUP-1(最近活动 ") {
		t.Fatalf("已授权群应带 last-seen: %q", out)
	}
	if !strings.Contains(out, "最近活动群(未授权;可 /im allowg 授权):") || !strings.Contains(out, "GROUP-2(最近活动 ") {
		t.Fatalf("未授权活动群应列出: %q", out)
	}
}
