package server

import (
	"errors"
	"log"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/driver"
	"newlist/internal/fs"
)

// OK 统一成功响应：HTTP 200 + {code:200, message:"success", data}。
func OK(c *gin.Context, data any) {
	c.JSON(200, gin.H{"code": 200, "message": "success", "data": data})
}

// Fail 统一失败响应：HTTP status 与 code 一致。
func Fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"code": status, "message": msg, "data": nil})
}

// Fail500 内部错误：完整详情落服务端日志，对外回「人话化」的原因。
// 自用定位，宁可露出真实原因也不要不透明的「服务器内部错误」——常见网络/超时/权限等
// 翻成人话，其余附上原始错误（见 humanize）。
// 请求方已挂断（ctx 取消）导致的失败不算服务端故障——ffmpeg/ffprobe 探测/抽帧
// 会频繁开关连接，在途请求被掐连带取消驱动 RPC（如 telegram rpcDoRequest:
// context canceled）——不记日志，回 499（客户端已关闭请求）即止。
func Fail500(c *gin.Context, err error) {
	if c.Request.Context().Err() != nil {
		Fail(c, 499, "客户端已断开")
		return
	}
	log.Printf("[500] %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	Fail(c, 500, humanize(err))
}

// humanize 把底层技术错误转成给用户看的人话；未归类的附上原始原因（好过不透明「服务器内部错误」）。
func humanize(err error) string {
	if err == nil {
		return "操作失败"
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded") ||
		strings.Contains(low, "timed out"):
		return "请求超时了：目标服务器响应太慢或网络不稳，请稍后重试"
	case strings.Contains(low, "no such host") || strings.Contains(low, "server misbehaving"):
		return "找不到目标服务器：域名解析失败，请检查地址或 DNS"
	case strings.Contains(low, "connection refused") || strings.Contains(low, "dial tcp") ||
		strings.Contains(low, "connectex") || strings.Contains(low, "network is unreachable"):
		return "连不上目标服务器：请检查网络、地址或代理设置"
	case strings.Contains(low, "connection reset") || strings.Contains(low, "broken pipe") ||
		strings.Contains(low, "unexpected eof") || low == "eof":
		return "连接中断了，请重试"
	case strings.Contains(low, "x509") || strings.Contains(low, "certificate") ||
		strings.Contains(low, "tls handshake"):
		return "HTTPS 证书校验失败：目标站点证书有问题或时间不同步"
	case strings.Contains(low, "no space left"):
		return "服务器磁盘空间不足"
	case strings.Contains(low, "permission denied"):
		return "没有权限：服务器本地文件/目录权限不足"
	case strings.Contains(low, "proxy"):
		return "代理连接失败：请检查代理设置是否可用"
	}
	// 兜底：附上真实原因（自用定位胜过一句空话）。
	return "操作失败：" + err.Error()
}

// fsError 把驱动/fs 层哨兵错误映射为 HTTP 状态码。
func fsError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, driver.ErrNotFound):
		Fail(c, 404, "对象不存在")
	case errors.Is(err, driver.ErrExist):
		Fail(c, 409, "目标已存在")
	case errors.Is(err, driver.ErrNotSupported):
		Fail(c, 501, "该存储不支持此操作")
	case errors.Is(err, driver.ErrBadName):
		Fail(c, 400, "名称包含非法字符或为保留名")
	case errors.Is(err, driver.ErrDenied):
		Fail(c, 403, "存储拒绝写入：该账号对此存储无写入权限，请重新授权（OneDrive 需 Files.ReadWrite）")
	case errors.Is(err, driver.ErrQuota):
		Fail(c, 507, "写入被拒（配额限制 quotaLimitReached）：即便显示有剩余空间也可能如此，多因账号未分配含 OneDrive 的许可证或站点存储配额受限，请检查该账号的 OneDrive 许可与配额")
	case errors.Is(err, driver.ErrUpstream):
		Fail(c, 502, err.Error()) // 云盘原始错误透传（自用定位，便于一眼定因）
	case errors.Is(err, fs.ErrBadPath):
		Fail(c, 400, "路径非法")
	default:
		Fail500(c, err)
	}
}
