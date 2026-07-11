package server

import (
	"errors"
	"log"

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

// Fail500 内部错误：详情落服务端日志，对外只回通用文案，
// 不向客户端泄露内部路径 / SQL / 驱动细节。
// 请求方已挂断（ctx 取消）导致的失败不算服务端故障——ffmpeg/ffprobe 探测/抽帧
// 会频繁开关连接，在途请求被掐连带取消驱动 RPC（如 telegram rpcDoRequest:
// context canceled）——不记日志，回 499（客户端已关闭请求）即止。
func Fail500(c *gin.Context, err error) {
	if c.Request.Context().Err() != nil {
		Fail(c, 499, "客户端已断开")
		return
	}
	log.Printf("[500] %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	Fail(c, 500, "服务器内部错误")
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
	case errors.Is(err, fs.ErrBadPath):
		Fail(c, 400, "路径非法")
	default:
		Fail500(c, err)
	}
}
