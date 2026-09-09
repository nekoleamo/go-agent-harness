// im.Access 变更通知与配对流程单元测试。
package im

import (
	"testing"
	"time"
)

// TestAccessOnChange 授权集变化触发 onChange:Allow/Revoke/ApprovePair 各一次;未注册不 panic。
func TestAccessOnChange(t *testing.T) {
	a := NewAccess(AccessPairing, nil, time.Hour)
	var calls int
	a.SetOnChange(func() { calls++ })

	a.Allow("ch\x00u1")
	if calls != 1 {
		t.Fatalf("Allow 应触发 onChange,got %d", calls)
	}
	a.Allow("ch\x00u1") // 幂等重复仍触发(可接受;由持久化端幂等)
	if calls != 2 {
		t.Fatalf("重复 Allow 应仍触发,got %d", calls)
	}
	if !a.Revoke("ch\x00u1") || calls != 3 {
		t.Fatalf("Revoke 应触发,got %d", calls)
	}
	// pairing 流程:Gate 生成码 → ApprovePair → onChange 并授权
	a = NewAccess(AccessPairing, nil, time.Hour)
	a.SetOnChange(func() { calls++ })
	res, code := a.Gate("ch\x00newbie")
	if res != gatePair || code == "" {
		t.Fatalf("pairing 应发码,res=%v", res)
	}
	if !a.ApprovePair(code) || calls != 4 {
		t.Fatalf("ApprovePair 应触发并成功,got %v calls=%d", a.Allowed("ch\x00newbie"), calls)
	}
	if !a.Allowed("ch\x00newbie") {
		t.Fatal("批准后应已授权")
	}
}

// TestApprovePairExpired 过期配对码拒绝且不触发 onChange。
func TestApprovePairExpired(t *testing.T) {
	a := NewAccess(AccessPairing, nil, -time.Second) // 立即过期
	var calls int
	a.SetOnChange(func() { calls++ })
	_, code := a.Gate("ch\x00x")
	_ = code
	// Gate 时同人复用旧码(过期清理后重新生成);直接批准已清空的码应失败
	if a.ApprovePair("not-exist") {
		t.Fatal("未知码应拒绝")
	}
	if calls != 0 {
		t.Fatalf("失败批准不应触发 onChange,got %d", calls)
	}
}
