// logtail:壳日志尾部(诊断页要用)。
//
// 为什么单独一层:真机上「窗口白着、机器上没痕迹」是最难查的形态(旧壳 2026-09-15/16
// 两轮白屏)。把日志尾部直接铺进窗口,用户截个图就能定位;而读整份日志会随文件增长变慢,
// 所以只读尾部 N 字节,并且**丢掉被截断的首行**(面板里不该出现残句)。
package main

import (
	"io"
	"os"
	"strings"
)

// shellLogTailBytes 读尾部字节上限(够放几十行,又不至于把整份日志读进来)。
const shellLogTailBytes = 64 << 10

// readTail 读文件尾部至多 max 字节;触发截断时丢掉首行残句。
func readTail(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	start := st.Size() - max
	if start < 0 {
		start = 0
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return ""
		}
	}
	buf := make([]byte, 0, max+4096)
	tmp := make([]byte, 8192)
	for {
		n, err := f.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	s := string(buf)
	if start > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

// shellLogTail 取最后 n 行(日志文件不存在时返回空串)。
func shellLogTail(n int) string {
	text := readTail(shellLogPath(), shellLogTailBytes)
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
