// P4-1 消息队列(State.Queue):回合运行中输入的消息排队、回合后自动续发的队列方法。
// 队列先进先发;取回(pop 尾)供 Alt+Up/Esc 恢复编辑。纯逻辑,可脱离终端单测。
package tui

// Enqueue 入队一条普通消息(回合运行中提交;命令不入队)。
func (s *State) Enqueue(text string) {
	s.Queue = append(s.Queue, text)
}

// Dequeue 取队列头(自动发送下一条;空队列返回 "" 不 panic)。
func (s *State) Dequeue() string {
	if len(s.Queue) == 0 {
		return ""
	}
	t := s.Queue[0]
	s.Queue = s.Queue[1:]
	return t
}

// PopQueued 取队列尾(最新一条,取回编辑区;空返回 "")。
func (s *State) PopQueued() string {
	if len(s.Queue) == 0 {
		return ""
	}
	t := s.Queue[len(s.Queue)-1]
	s.Queue = s.Queue[:len(s.Queue)-1]
	return t
}

// ClearQueue 清空队列(会话/工作区切换时丢弃旧队列,防错发到新会话)。
func (s *State) ClearQueue() { s.Queue = nil }
