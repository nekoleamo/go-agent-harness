package sdk

// 维护类操作的空闲闸门(第一百六十七批)。
//
// 钉三件会悄悄错的事:
//  ① 回合运行中 ⇒ 拒绝,且理由要能直接给用户看(说清等什么);
//  ② 没有运行回合 ⇒ 放行;
//  ③ **未装配** ctx.turnControl ⇒ 放行(嵌入/单测/极简宿主不该被"忙"挡住,也不能
//     假装忙 —— 那会让运维命令在没有回合控制的宿主里永远不可用)。
import (
	"errors"
	"strings"
	"testing"
)

// maintCtx 在公共 fakeCtx 上只覆盖 Inject:能给出 ctx.turnControl。
type maintCtx struct {
	fakeCtx
	tc TurnControl
}

func (m *maintCtx) Inject(key string, dst any) error {
	if key != "ctx.turnControl" || m.tc == nil {
		return errors.New("未装配")
	}
	*(dst.(*TurnControl)) = m.tc
	return nil
}

// fakeTurn 固定返回 Running 的回合控制。
type fakeTurn struct{ running bool }

func (f fakeTurn) Running() bool { return f.running }
func (f fakeTurn) Cancel()       {}

func TestMaintenanceBlockedWhileTurnRunning(t *testing.T) {
	reason, blocked := MaintenanceBlocked(&maintCtx{tc: fakeTurn{running: true}})
	if !blocked {
		t.Fatal("回合进行中应拒绝")
	}
	if reason == "" {
		t.Fatal("拒绝必须给出可展示的原因(不能只回一个 false)")
	}
	// 原因里要有出口:告诉用户等什么 / 按什么,否则等于"不许做"而没有下一步。
	if !strings.Contains(reason, "/stop") {
		t.Fatalf("原因要说清出口(等回合结束或 /stop): %q", reason)
	}
}

func TestMaintenanceAllowedWhenIdle(t *testing.T) {
	if _, blocked := MaintenanceBlocked(&maintCtx{tc: fakeTurn{running: false}}); blocked {
		t.Fatal("无运行回合不该拒绝")
	}
}

func TestMaintenanceAllowedWithoutTurnControl(t *testing.T) {
	if _, blocked := MaintenanceBlocked(&maintCtx{}); blocked {
		t.Fatal("未装配 ctx.turnControl 不该假装忙")
	}
	if _, blocked := MaintenanceBlocked(nil); blocked {
		t.Fatal("nil ctx 不该拒绝(嵌入/测试路径)")
	}
}
