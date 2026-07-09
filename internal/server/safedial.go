package server

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// safeControl 在每次拨号（含重定向后每一跳）DNS 解析后校验目标 IP，拒绝
// 回环 / 私有 / 链路本地 / ULA / 未指定 / 组播地址——防离线下载被用来打内网
// 服务或云元数据端点（169.254.169.254 属链路本地，被 IsLinkLocalUnicast 拦下）。
// 作为 net.Dialer.Control 挂入：此时 address 已是解析后的具体 IP:port，
// 因此对每一跳（含 302 重定向后的新连接）都会生效。
func safeControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("目标地址无法解析为 IP: %s", address)
	}
	ip = ip.Unmap() // 归一 IPv4-mapped IPv6，避免 ::ffff:10.0.0.1 绕过判定
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return fmt.Errorf("拒绝访问内网/保留地址: %s", ip)
	}
	return nil
}

// offlineDialControl 是离线下载拨号时的 IP 校验钩子；生产恒为 safeControl，
// 测试可临时替换以放行 httptest 的回环上游（httptest 均监听 127.0.0.1）。
var offlineDialControl = safeControl

// offlineDialer 构造带 SSRF 校验的拨号器（Control 走可替换的 offlineDialControl）。
func offlineDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			return offlineDialControl(network, address, c)
		},
	}
}
