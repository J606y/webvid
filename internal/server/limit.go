package server

import (
	"crypto/subtle"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// limitedResponseWriter 给下行响应套限速。只内嵌 http.ResponseWriter 接口，
// 刻意不提升底层的 ReadFrom/Flush 等方法——io.Copy 便走普通 Write 进入限速路径。
type limitedResponseWriter struct {
	http.ResponseWriter
	w io.Writer // limiter 包装后的写入器
}

func (lw *limitedResponseWriter) Write(p []byte) (int, error) { return lw.w.Write(p) }

// isInternal 判定请求来自本进程内部读取方（ffmpeg/ffprobe 拉 /api/raw 转码/探测），
// 凭 X-Internal-Auth 头——不信来源 IP：反代后所有请求 RemoteAddr 均为回环，
// 靠 IP 判定会让真实用户的请求被误判内部。
func (s *Server) isInternal(c *gin.Context) bool {
	tok := s.media.InternalToken()
	return tok != "" &&
		subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Auth")), []byte(tok)) == 1
}

// downloadWriter 返回套了全站下载限速的响应写入器；内部回环请求豁免限速。
func (s *Server) downloadWriter(c *gin.Context) http.ResponseWriter {
	if s.isInternal(c) {
		return c.Writer
	}
	// 恒包装（Write 内部按当前速率判断）：限速值热调整对在途长流也即时生效
	return &limitedResponseWriter{
		ResponseWriter: c.Writer,
		w:              s.limDown.Writer(c.Request.Context(), c.Writer),
	}
}
