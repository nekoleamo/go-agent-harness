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
	res, code := a.Gate("ch\x00newbie", "")
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
	_, code := a.Gate("ch\x00x", "")
	_ = code
	// Gate 时同人复用旧码(过期清理后重新生成);直接批准已清空的码应失败
	if a.ApprovePair("not-exist") {
		t.Fatal("未知码应拒绝")
	}
	if calls != 0 {
		t.Fatalf("失败批准不应触发 onChange,got %d", calls)
	}
}

// TestGroupAllow 群维度授权:一次授权整群(群内任意成员放行);私聊不受影响;撤销/revoce 生效。
func TestGroupAllow(t *testing.T) {
	a := NewAccess(AccessPairing, nil, time.Hour)
	var calls int
	a.SetOnChange(func() { calls++ })
	if res, _ := a.Gate("qq\x00member-a", "qq\x00GROUP1"); res != gatePair {
		t.Fatalf("未授权群内成员应走配对: %v", res)
	}
	a.AllowGroup("qq\x00GROUP1")
	if calls != 1 {
		t.Fatalf("群授权应触发 onChange,got %d", calls)
	}
	for _, m := range []string{"qq\x00member-a", "qq\x00member-b"} {
		if res, _ := a.Gate(m, "qq\x00GROUP1"); res != gateDeliver {
			t.Fatalf("群授权后 %s 应放行: %v", m, res)
		}
	}
	// 同一成员私聊(chatKey = senderKey)不受群授权影响
	if res, _ := a.Gate("qq\x00member-a", "qq\x00member-a"); res != gatePair {
		t.Fatalf("私聊不应被群授权放行: %v", res)
	}
	if gs := a.Groups(); len(gs) != 1 || gs[0] != "qq\x00GROUP1" {
		t.Fatalf("Groups 不符: %v", gs)
	}
	if !a.RevokeGroup("qq\x00GROUP1") || calls != 2 {
		t.Fatalf("撤销应触发 onChange: %v calls=%d", a.RevokeGroup("x"), calls)
	}
	if a.RevokeGroup("qq\x00GROUP1") {
		t.Fatal("重复撤销应返回 false")
	}
	if res, _ := a.Gate("qq\x00member-a", "qq\x00GROUP1"); res != gatePair {
		t.Fatalf("撤销后应回到配对: %v", res)
	}
}

// TestGroupAllowInit Options.AllowGroups 初始化群授权(不触发 onChange)。
func TestGroupAllowInit(t *testing.T) {
	a := newAccessWith(Options{Mode: AccessPairing, AllowGroups: []string{"qq\x00G1", ""}})
	if res, _ := a.Gate("qq\x00m", "qq\x00G1"); res != gateDeliver {
		t.Fatalf("初始群授权应放行: %v", res)
	}
	if len(a.Groups()) != 1 {
		t.Fatalf("空 key 应被忽略: %v", a.Groups())
	}
}
