// Package hostbridge 提供 host-bridge 插件:外部 gRPC/进程插件桥(崩溃隔离,对齐设计 §2.3/M5)。
// 协议:go-plugin net/rpc 模式(免 protoc);外部插件 = 独立进程二进制,崩溃不拖垮宿主。
package hostbridge

import "github.com/hashicorp/go-plugin"

// handshake 宿主与外部插件的协议标识(必须与 extplugins 一致)。
var handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "GAH_PLUGIN",
	MagicCookieValue: "gah-external-tool",
}

// Handshake 返回握手配置(供外部插件引用)。
func Handshake() plugin.HandshakeConfig { return handshake }

// pluginName 协议插件名。
const pluginName = "tool"

// ToolServer 外部插件侧实现的 RPC 服务(net/rpc 方法签名)。
type ToolServer interface {
	Definition(args struct{}, reply *string) error
	Execute(args *ExecArgs, reply *ExecReply) error
}

// ExecArgs/ExecReply RPC 载荷。strings 传输(JSON),gob 可序列化。
type ExecArgs struct {
	JSONArgs string
}

type ExecReply struct {
	Content string // 结果 JSON/文本
	Error   string // 结构化错误(非空 = 失败,不中断 turn)
}
