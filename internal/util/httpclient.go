package util

import (
	"net"
	"net/http"
	"time"
)

// NewHTTPTransport 建一条给云盘驱动、缩略图下载这类「同一个 host 反复调用」的传输层。
//
// http.DefaultTransport 每 host 只留 2 条空闲连接，总连接数不限：一轮预载或主页一屏
// 封面会朝云盘开出几百条 TCP+TLS，办完事只留下 2 条可复用，其余全进 TIME_WAIT——
// 连接数瞬间冲高、迟迟下不来，正是把服务压垮的那个形状。留够空闲连接就没这回事。
//
// 时限只加在握手与响应头上，body 不设限：大文件上传下载正是要在 body 上耗时间，
// 给 http.Client 设一个全局 Timeout 会把它们一并砍断。真正的取消交给调用方的 ctx。
func NewHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2: true,
		// 上限按转存的最大并发（copy_file_workers 至多 32）留，够用且不会无限张开；
		// 空闲连接留 16 条，日常的列目录、取直链、下封面全在这几条上复用。
		MaxConnsPerHost:       32,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
