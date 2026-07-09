package server

import "testing"

// TestSafeControlBlocksInternalAddresses 覆盖 safeControl 的 SSRF 防护：回环 / 私有 /
// 链路本地（含云元数据端点）/ ULA / 未指定 / 组播地址均应被拒绝，公网地址应放行。
func TestSafeControlBlocksInternalAddresses(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "IPv4 loopback", addr: "127.0.0.1:80", wantErr: true},
		{name: "RFC1918 10.x", addr: "10.1.2.3:80", wantErr: true},
		{name: "RFC1918 172.16.x", addr: "172.16.0.1:80", wantErr: true},
		{name: "RFC1918 192.168.x", addr: "192.168.1.1:80", wantErr: true},
		{name: "cloud metadata link-local", addr: "169.254.169.254:80", wantErr: true},
		{name: "IPv6 loopback", addr: "[::1]:80", wantErr: true},
		{name: "IPv6 ULA", addr: "[fc00::1]:80", wantErr: true},
		{name: "unspecified", addr: "0.0.0.0:80", wantErr: true},
		{name: "multicast", addr: "224.0.0.1:80", wantErr: true},

		{name: "public IPv4 google dns", addr: "8.8.8.8:443", wantErr: false},
		{name: "public IPv4 cloudflare dns", addr: "1.1.1.1:80", wantErr: false},
		{name: "public IPv6 cloudflare dns", addr: "[2606:4700:4700::1111]:443", wantErr: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := safeControl("tcp", c.addr, nil)
			if c.wantErr && err == nil {
				t.Fatalf("safeControl(%q) 应返回 error，实际 nil", c.addr)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("safeControl(%q) 应返回 nil，实际 %v", c.addr, err)
			}
		})
	}
}
