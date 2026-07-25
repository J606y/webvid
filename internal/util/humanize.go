package util

import (
	"strings"
	"unicode"
)

// Humanize 把底层技术错误转成一句给用户看的中文，口吻对标 Apple 软件的错误提示：
// 说清「什么错 + 一句怎么办」，克制、不甩英文/jargon/Go 类型名。
//
// 先按已知的网络/系统/接口关键词映射成干净中文（即便这些英文夹在中文外壳里，
// 也能命中并替换）；都没命中时：本身已含中文说明就原样返回（多为项目自己写的、
// 已经说清的错误），纯英文/技术串才包一层，至少点明是失败。
//
// 调用方仍应把原始 err 写进服务端日志，方便排查——这里只负责「给人看」。
func Humanize(err error) string {
	if err == nil {
		return "操作失败"
	}
	s := strings.TrimSpace(err.Error())
	low := strings.ToLower(s)
	switch {
	// —— 安全策略（要排在网络之前）——
	// SSRF 防护拒绝内网地址时，错误串里裹着 "dial tcp"，会被下面的网络分支吞成
	// 「无法连接到服务器」——用户便以为是网速问题反复重试，真正的原因反而没了。
	case has(s, "拒绝访问内网", "保留地址"):
		return "不支持内网或本机地址。离线下载只能拉取公网可访问的链接。"
	// —— 网络 ——
	case has(low, "timeout", "deadline exceeded", "timed out", "i/o timeout"):
		return "连接超时。请检查网络后重试。"
	case has(low, "no such host", "server misbehaving"):
		return "无法解析服务器地址。请检查网址或 DNS 设置。"
	case has(low, "connection refused", "dial tcp", "connectex", "network is unreachable", "no route to host"):
		return "无法连接到服务器。请检查网络或地址。"
	case low == "eof" || has(low, "connection reset", "broken pipe", "unexpected eof", "connection closed"):
		return "连接中断，请重试。"
	case has(low, "x509", "certificate", "tls handshake", "tls: "):
		return "无法验证服务器的安全证书。请检查系统时间是否正确。"
	case has(low, "socks", "proxy"):
		return "无法连接代理服务器。请检查代理设置。"
	// —— 系统 ——
	case has(low, "no space left"):
		return "服务器存储空间不足。"
	case has(low, "permission denied", "access is denied", "operation not permitted"):
		return "服务器文件权限不足，无法访问。"
	// —— 接口 / 授权（云盘、OAuth 常见）——
	case has(low, "http 429", "too many requests", "flood_wait", "rate limit", "操作频繁", "操作过于频繁"):
		return "请求过于频繁，请稍后重试。"
	case has(low, "http 401", "invalid_grant", "unauthorized", "token expired", "token 已失效", "token 失效"):
		return "登录授权已失效，请重新授权后重试。"
	case has(low, "http 403", "forbidden", "accessdenied", "access_denied"):
		return "没有权限执行此操作，可能需要重新授权。"
	case has(low, "http 404", "not found", "notfound"):
		return "找不到对应的文件或资源。"
	}
	if containsCJK(s) {
		return s
	}
	return "操作失败：" + s
}

// has 报告 s 是否包含 subs 中任意一个子串。
func has(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// containsCJK 判断字符串是否含中日韩汉字——用来区分「项目自己写的中文错误」与「原始英文错误」。
func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
