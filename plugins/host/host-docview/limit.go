package hostdocview

import (
	"io"
	"strings"
)

// converterOutputLimit 外部转换器 stdout/stderr 捕获上限:损坏/恶意文档可让
// soffice/pdftoppm 无限刷日志,无上限字符串缓冲会吃光内存(失败信息只需尾部一条)。
const converterOutputLimit = 64 << 10

// limitedBuilder 带上限的写入缓冲:超出上限后丢弃后续内容(仍按"已消费"返回,
// 避免 exec 因短写报错)。用于外部进程输出捕获。
type limitedBuilder struct {
	b     *strings.Builder
	limit int
}

func (l *limitedBuilder) Write(p []byte) (int, error) {
	n := len(p)
	if room := l.limit - l.b.Len(); room > 0 {
		if n > room {
			n = room
		}
		_, _ = l.b.Write(p[:n])
	}
	return len(p), nil
}

// limitedReadCloser 给 ReadCloser 套读取上限(Close 仍作用于原 reader)。
type limitedReadCloser struct {
	rc io.ReadCloser
	lr io.Reader
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.lr.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.rc.Close() }
