package server

import (
	"errors"
	"strings"
	"testing"
)

func TestHumanize(t *testing.T) {
	cases := []struct {
		in   string
		want string // 期望包含的人话关键词
	}{
		{"Get \"https://x\": context deadline exceeded", "超时"},
		{"dial tcp 1.2.3.4:443: i/o timeout", "超时"},
		{"dial tcp: lookup foo.bar: no such host", "找不到目标服务器"},
		{"dial tcp 1.2.3.4:443: connect: connection refused", "连不上目标服务器"},
		{"read: connection reset by peer", "连接中断"},
		{"unexpected EOF", "连接中断"},
		{"x509: certificate signed by unknown authority", "证书"},
		{"write /data/x: no space left on device", "磁盘空间不足"},
		{"open /etc/x: permission denied", "没有权限"},
		{"socks connect tcp: proxyconnect", "代理"},
		{"sql: database is locked", "操作失败：sql: database is locked"}, // 未归类→附原文
	}
	for _, c := range cases {
		got := humanize(errors.New(c.in))
		if !strings.Contains(got, c.want) {
			t.Errorf("humanize(%q) = %q，应包含 %q", c.in, got, c.want)
		}
	}
	if got := humanize(nil); got != "操作失败" {
		t.Errorf("humanize(nil) = %q", got)
	}
}
