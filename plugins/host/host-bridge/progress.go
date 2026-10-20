// progress.go:工具执行**增量输出**的跨进程回传(2026-10-20 优化批次 C1)。
//
// 为什么需要跨进程:发行态默认把 shell 等工具**外部化**(tool-kit 进程),
// 工具的 stdout/stderr 产生在那个进程里 —— 宿主拿不到它的中间输出,
// 于是"跑 3 分钟的构建"在界面上只有一个转圈:看不出在动、在等什么、是不是卡住了。
//
// 链路(三个角色,职责分明):
//
//	宿主 toolRPCClient.Execute ──(ExecNamedArgs.ProgressToken)──▶ 外部进程 toolServer.exec
//	外部进程 shell 工具 ──sdk.ReportToolProgress(ctx)──▶ 外部进程 reporter
//	外部进程 reporter ──(回调通道 progress/record)──▶ 宿主 Callback ──▶ 宿主查表还原归属
//	宿主 ──sdk.EventToolProgress──▶ TUI / Web(只走 UI 通道,不进会话日志)
//
// 为什么用**令牌**而不是直接把模型侧 call id 传给插件:
//   - 归属(会话 + 模型侧 call id)只有宿主知道,插件回传时只需带一个不透明的把手;
//   - 令牌是宿主随机生成并登记的 ⇒ 插件无法伪造别的调用的归属(否则一个插件能把
//     增量输出挂到别人的工具行上);
//   - 调用结束即注销 ⇒ 迟到的块无处可挂(静默丢弃,不报错:工具已经成功返回了)。
//
// 进程内工具(未外部化)不走这条路:agent-loop 直接把 reporter 装到 ctx 上,
// 产出的事件与跨进程路径**完全同形**(同一载荷类型、同一归属口径)。
package hostbridge

import (
	"encoding/json"
	"errors"
	"os"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 回调通道上的进度服务名与方法名(外部进程 → 宿主)。
const (
	progressService = "progress"
	progressRecord  = "record"
)

// progressRecordPayload 外部进程回传的一段增量输出。
//
// 只带令牌与文本:归属(会话 / 模型侧 call id / 工具名)由宿主查表还原 ——
// 插件说了不算(见文件头"为什么用令牌")。
type progressRecordPayload struct {
	Token string `json:"token"`
	Chunk string `json:"chunk"`
}

// progOwner 一次外部工具调用的进度归属(宿主登记)。
type progOwner struct {
	rep  sdk.ToolProgressReporter // 上报出口(agent-loop 装进 ctx 的那个:自带会话/调用 id 归属)
	name string                   // 工具名(回传的块要挂到哪一行)
}

// registerProgress 登记一次外部调用的进度归属(令牌 → 归属)。
//
// 令牌由宿主随机生成:插件拿到它也只能回传"这次调用的增量",不能指定别的归属。
//
// 归属(rep)取自调用 ctx 里 agent-loop 装好的上报出口 —— 它闭包捕获了那一回合的 ctx,
// 于是"会话 + 模型侧 call id + 序号"都在里面,宿主侧不必再抄一遍归属口径(抄一遍就会漂)。
func (b *Bridge) registerProgress(token string, rep sdk.ToolProgressReporter, name string) {
	if token == "" || rep == nil {
		return
	}
	b.progMu.Lock()
	defer b.progMu.Unlock()
	if b.progOwners == nil {
		b.progOwners = map[string]progOwner{}
	}
	b.progOwners[token] = progOwner{rep: rep, name: name}
}

// unregisterProgress 注销令牌(调用结束):迟到的块无处可挂 ⇒ 静默丢弃。
func (b *Bridge) unregisterProgress(token string) {
	if token == "" {
		return
	}
	b.progMu.Lock()
	defer b.progMu.Unlock()
	delete(b.progOwners, token)
}

// handleProgress 处理外部进程回传的一段增量输出:查表还原归属 → 交给该调用的上报出口。
//
// 查不到令牌 = 调用已结束(迟到块)或令牌非法 ⇒ **静默丢弃**:
// 进度是锦上添花,工具本身已经成功返回,此时再报错只会制造噪音。
func (b *Bridge) handleProgress(token, chunk string) error {
	if token == "" || chunk == "" {
		return nil
	}
	b.progMu.Lock()
	own, ok := b.progOwners[token]
	b.progMu.Unlock()
	if !ok {
		return nil
	}
	return own.rep(own.name, chunk)
}

// progressSender 外部进程侧:把增量输出经**宿主回调通道**回传。
//
// 惰性拨号:插件进程启动时未必需要进度通道(宿主没开回调通道时 addr 为空 ⇒ 直接禁用)。
type progressSender struct {
	mu     sync.Mutex
	cc     *CallbackClient
	failed bool // 拨号失败过(不再重试:回调通道没开时反复拨号只是噪音)
}

// send 回传一段增量输出(失败只返回错误:调用方 sdk.ReportToolProgress 忽略它)。
//
// 地址**懒读**环境变量(不在包初始化时读):插件进程的环境是宿主 spawn 时注入的,
// 而包初始化早于任何注入动作 —— 早读会永远拿到空地址。
func (s *progressSender) send(token, chunk string) error {
	addr := os.Getenv("GAH_CB_ADDR")
	if addr == "" {
		return errors.New("progress: 宿主未注入回调通道地址,增量输出无法回传")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("progress: 回调通道不可用,增量输出不再回传")
	}
	if s.cc == nil {
		cc, err := DialCallback(addr)
		if err != nil {
			s.failed = true
			return err
		}
		s.cc = cc
	}
	var reply string
	if err := s.cc.Call(progressService, progressRecord, progressRecordPayload{Token: token, Chunk: chunk}, &reply); err != nil {
		return err
	}
	if reply != "" {
		return errors.New(reply)
	}
	return nil
}

// progressSenderFor 外部进程侧单例(一个插件进程一个回调连接)。
var progressSenderFor = &progressSender{}

// progressReporter 生成装进工具 ctx 的上报出口(带本次调用的令牌)。
//
// 令牌为空 = 宿主没开进度通道(老宿主)⇒ 返回 nil,工具照常工作(无进度)。
func progressReporter(token string) sdk.ToolProgressReporter {
	if token == "" {
		return nil
	}
	return func(tool, chunk string) error {
		if chunk == "" {
			return nil
		}
		return progressSenderFor.send(token, chunk)
	}
}

// marshalProgressRecord 序列化一段回传载荷(测试与调试用)。
func marshalProgressRecord(token, chunk string) (string, error) {
	b, err := json.Marshal(progressRecordPayload{Token: token, Chunk: chunk})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
