// diagnostics.go:请求前缀稳定性探针(只读诊断,查缓存命中率为什么低)。
//
// 背景(2026-10-05 用户反馈:「缓存命中率只有 20~30%,token 花费过大」):
// 提示缓存是**前缀命中** —— 厂商按「与上一次请求完全相同的最长前缀」给折扣。所以命中率低
// 只有两种可能,而它们的处置完全相反:
//
//	A. 前缀在两次请求之间**变了**(某段内容被重排/改写/裁剪)⇒ 改代码,让前缀稳定;
//	B. 前缀稳定但仍然没命中 ⇒ 大概率是前缀本身太短(不足最小命中块)或厂商侧缓存过期,
//	   改代码没用,该考虑减少单次体积。
//
// 光看累计命中率**分不出 A 与 B**(它只是两个数字相除)。所以这里给一个只读的探针:
// 每次组装请求时取 system + 工具 + 历史的指纹,记一条样本,并与上一条比对,指出
// 「第一条变化的��息是什么」。跑一轮真实会话,答案就在输出里 —— 这比先猜再改可靠得多。
//
// **零开销声明**:只算一次哈希(system + 每条消息的 role/长度/前若干字节),不做全量序列化,
// 不缓存任何正文;样本数有上限(见 agent-loop 侧),不随会话增长。
package sdk

// PrefixSample 一次请求的前缀指纹样本。
type PrefixSample struct {
	// Seq 本会话内第几次请求(从 1 起)。
	Seq int
	// At 发生时刻(RFC3339 或 HH:MM:SS,取决于调用方装配方式;诊断只读不改语义)。
	At string
	// Hash 前缀指纹(system + 工具名 + 历史)的前 8 位十六进制。
	Hash string
	// SysChars 系统提示的字符数。
	SysChars int
	// MsgCount 组装后的消息条数(含 system)。
	MsgCount int
	// DiffToPrev 与上一条样本的关系:"same" = 指纹一致 | "changed" = 变了 |
	// "first" = 本会话第一条样本(没有可比的上一条)。
	DiffToPrev string
	// DiffWhere 变化点(仅 DiffToPrev = "changed" 时有值):第几条消息开始不同、
	// 它的角色、以及开头一小段文字。定位"是谁在动前缀"靠的就是这一行。
	DiffWhere string
}

// PrefixProbe 前缀稳定性探针(host-agent-loop 提供;未装配时诊断命令如实说明)。
type PrefixProbe interface {
	// PrefixSamples 返回最近的样本(时间正序);limit<=0 取默认值。
	PrefixSamples(limit int) []PrefixSample
	// PrefixTurns 本会话累计发出的模型请求数(与样本条数无关:样本有上限)。
	PrefixTurns() int
}
