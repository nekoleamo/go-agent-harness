// Package acpclient:ACP(Agent Client Protocol)**出站**客户端 —— 把编码任务委派给
// 外部编码 agent(opencode / codex / claude-code / kimi / cursor …),stdio 上跑
// JSON-RPC 2.0(每行一条消息)。
//
// 为什么要有它(第一百六十六批,借鉴 Octop 的 acp_runner):gah 已有 acp-server
// (**入站**:被 Zed 这类编辑器驱动)。入站只解决"别人用 gah",不解决"gah 用别人"——
// 而要跑一个长时编码任务(改一个仓库、装依赖、跑测试),外部专业 agent 往往比
// 在 gah 里逐条喂 shell 更省上下文也更稳。这一层把那个出口接上,权限请求仍走 gah
// 自己的审批闸(见 tool.go 的 mapPermission),不是另开一道放行。
//
// 协议面无依赖:仅 stdio + 换行分隔的 JSON-RPC,和 mcp-server/mcp-bridge 同族。
package acpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// rpcMsg 一条 JSON-RPC 2.0 消息(请求/响应/通知共用)。
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

// rpcErr JSON-RPC 错误体。
type rpcErr struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcErr) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("ACP %d: %s", e.Code, e.Message)
}

// callbacks 对端主动发来的请求/通知的处理面(由工具层注入)。
type callbacks struct {
	// onRequest 处理 agent → client 的请求(目前只有 session/request_permission)。
	// 返回 (结果, 错误);错误会作为 JSON-RPC error 回给对端。
	onRequest func(ctx context.Context, method string, params json.RawMessage) (any, *rpcErr)
	// onNotify 处理 agent → client 的通知(session/update 的各类增量)。
	onNotify func(method string, params json.RawMessage)
}

// conn 一条 stdio JSON-RPC 连接(读循环 + 待决请求表)。
//
// 并发模型:call 可以并发(各自等自己的 id);读循环单独一条 goroutine;
// 请求/通知回调在**独立 goroutine**里跑 —— 不能在读循环里跑,否则 onRequest 里
// 一次"等用户审批"就把整条连接卡死(增量更新全部停摆)。
type conn struct {
	w  io.Writer
	cb callbacks

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMsg
	closed  bool

	done  chan struct{} // 读循环结束
	errMu sync.Mutex
	err   error // 读循环失败原因(结束后所有 call 返回它)
}

// newConn 建连接并启动读循环(r 通常是子进程 stdout)。
func newConn(w io.Writer, r io.Reader, cb callbacks) *conn {
	c := &conn{w: w, cb: cb, pending: map[int64]chan rpcMsg{}, done: make(chan struct{})}
	go c.readLoop(r)
	return c
}

// readLoop 逐行解码并分发。EOF/解析失败 ⇒ 结束,并把错误传播给所有待决调用
// (不能让调用方永久等待:子进程崩了必须让这一轮显式失败)。
func (c *conn) readLoop(r io.Reader) {
	defer close(c.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // 单条 8MB:一大段代码 chunk 可能不小
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m rpcMsg
		if err := json.Unmarshal(line, &m); err != nil {
			// 非协议行(某些 CLI 会往 stdout 打 banner/日志):忽略,不当成致命错误
			// —— 但也不静默:交给 onNotify 之外的诊断口(见 tool 侧的 stderr 收集)。
			continue
		}
		c.dispatch(m)
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.fail(err)
}

// dispatch 按消息形态分发:有 Method 的是对端发来的(请求/通知),否则是响应。
func (c *conn) dispatch(m rpcMsg) {
	switch {
	case m.Method != "" && m.ID != nil:
		go c.serveRequest(m)
	case m.Method != "":
		// 通知在**读循环内同步**处理:增量与本轮结果必须保序 —— 对端先发正文 chunk、
		// 后发 response,两条处理路径若并发,tool 层可能在 chunk 还没累加时就把 output
		// 读走了(实测抓到:output 恒为空)。
		//
		// 代价:onNotify 里**不能**反向调 conn.call(那会等自己回包 ⇒ 死锁)。
		// 通知面只做累加与进度上报,满足这个约束。
		if c.cb.onNotify != nil {
			c.cb.onNotify(m.Method, m.Params)
		}
	case m.ID != nil:
		c.mu.Lock()
		ch := c.pending[*m.ID]
		delete(c.pending, *m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m // 缓冲 1:写入方不会被接收方节奏卡住
		}
	}
}

// serveRequest 处理对端请求并回包(含错误)。
func (c *conn) serveRequest(m rpcMsg) {
	var out any
	var rerr *rpcErr
	if c.cb.onRequest == nil {
		rerr = &rpcErr{Code: -32601, Message: "unsupported request: " + m.Method}
	} else {
		// 对端请求没有本地的 ctx 取消来源:用 Background(子进程被杀时连接会断,
		// 读循环结束 ⇒ 回调里的确认通道由 UI 侧超时/取消兜底)。
		out, rerr = c.cb.onRequest(context.Background(), m.Method, m.Params)
	}
	resp := rpcMsg{JSONRPC: "2.0", ID: m.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		b, err := json.Marshal(out)
		if err != nil {
			resp.Error = &rpcErr{Code: -32603, Message: "marshal result: " + err.Error()}
		} else {
			resp.Result = b
		}
	}
	_ = c.write(resp)
}

// write 写一条消息(加锁:并发请求不能把两行交错)。
func (c *conn) write(m rpcMsg) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("ACP 连接已关闭")
	}
	_, err = c.w.Write(b)
	return err
}

// call 发请求并等响应;result 非 nil 时反序列化到它。
func (c *conn) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("ACP 连接已关闭")
		}
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	raw, err := json.Marshal(params)
	if err != nil {
		c.drop(id)
		return err
	}
	if err := c.write(rpcMsg{ID: &id, Method: method, Params: raw}); err != nil {
		c.drop(id)
		return err
	}

	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("ACP %s: 解析响应失败: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		return fmt.Errorf("ACP %s: 连接已断开: %w", method, c.failure())
	case <-ctx.Done():
		c.drop(id)
		return ctx.Err()
	}
}

// drop 撤销一条待决请求(超时/取消后不该再占着表项)。
func (c *conn) drop(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// fail 读循环结束:记下原因并唤醒所有待决调用。
func (c *conn) fail(err error) {
	c.mu.Lock()
	if !c.closed {
		c.err = err
		c.closed = true
	}
	pending := c.pending
	c.pending = map[int64]chan rpcMsg{}
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcMsg{Error: &rpcErr{Code: -32000, Message: "连接已断开: " + err.Error()}}:
		default:
		}
	}
}

// failure 读循环失败原因(可能为 nil:对端正常退出)。
func (c *conn) failure() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// close 主动关闭(不再接受新调用;读循环仍会自行结束)。
func (c *conn) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}
