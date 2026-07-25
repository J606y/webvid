package util

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
		// 安全策略：必须压过网络分支，否则被吞成「无法连接到服务器」，
		// 用户只会以为是网速问题反复重试
		{"Get \"http://127.0.0.1:5321/dl\": dial tcp 127.0.0.1:5321: 拒绝访问内网/保留地址: 127.0.0.1",
			"不支持内网或本机地址"},
		// 网络
		{"Get \"https://x\": context deadline exceeded", "超时"},
		{"dial tcp 1.2.3.4:443: i/o timeout", "超时"},
		{"dial tcp: lookup foo.bar: no such host", "无法解析服务器地址"},
		{"dial tcp 1.2.3.4:443: connect: connection refused", "无法连接到服务器"},
		{"read: connection reset by peer", "连接中断"},
		{"unexpected EOF", "连接中断"},
		{"x509: certificate signed by unknown authority", "证书"},
		{"socks connect tcp: proxyconnect", "代理"},
		// 系统
		{"write /data/x: no space left on device", "存储空间不足"},
		{"open /etc/x: permission denied", "权限不足"},
		// 接口 / 授权
		{"googledrive: 授权失败(HTTP 400): invalid_grant", "授权已失效"},
		{"pikpak: 请求失败(HTTP 429): too many requests", "过于频繁"},
		{"rpc error: FLOOD_WAIT (86400)", "过于频繁"},
		{"onedrive: accessDenied", "没有权限"},
		{"upstream returned HTTP 404", "找不到"},
		// 未归类的纯英文 → 包一层，至少点明是失败
		{"sql: database is locked", "操作失败：sql: database is locked"},
	}
	for _, c := range cases {
		if got := Humanize(errors.New(c.in)); !strings.Contains(got, c.want) {
			t.Errorf("Humanize(%q) = %q，应包含 %q", c.in, got, c.want)
		}
	}

	// 项目自己写的中文错误：原样返回，不再套「操作失败：」外壳
	zh := "存储空间已满，写入失败"
	if got := Humanize(errors.New(zh)); got != zh {
		t.Errorf("中文错误应原样返回，得到 %q", got)
	}

	if got := Humanize(nil); got != "操作失败" {
		t.Errorf("Humanize(nil) = %q", got)
	}
}
