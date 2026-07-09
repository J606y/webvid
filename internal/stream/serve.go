package stream

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// httpRange 客户端请求的单区间（只支持单区间——主流播放器/下载器均如此）。
type httpRange struct {
	start, length int64
}

// parseRange 解析 Range 头。返回：区间、是否带 Range、是否可满足。
// 语法错误按 RFC 7233 视为无 Range（返回全量）；语义不可满足（起点越界等）→ satisfiable=false。
func parseRange(h string, size int64) (r httpRange, hasRange, satisfiable bool) {
	if h == "" {
		return httpRange{0, size}, false, true
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return httpRange{0, size}, false, true // 多区间/非 bytes 单位：按无 Range 处理
	}
	spec = strings.TrimSpace(spec)
	dash := strings.Index(spec, "-")
	if dash < 0 {
		return httpRange{0, size}, false, true
	}
	a, b := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	if a == "" { // bytes=-n 末尾 n 字节
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return httpRange{}, true, false
		}
		if n > size {
			n = size
		}
		return httpRange{size - n, n}, true, true
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 || start >= size {
		return httpRange{}, true, false
	}
	if b == "" { // bytes=a-
		return httpRange{start, size - start}, true, true
	}
	end, err := strconv.ParseInt(b, 10, 64)
	if err != nil || end < start {
		return httpRange{}, true, false
	}
	if end > size-1 {
		end = size - 1
	}
	return httpRange{start, end - start + 1}, true, true
}

// Serve 以代理模式响应下载/播放请求：解析客户端 Range → 并发分块拉直链 → 单流回给客户端。
// size 未知（<0）时退化为单流透传（原样转发 Range 头、镜像上游状态与长度头）。
// 调用方先设好 Content-Disposition 等附加头再进来。
func Serve(w http.ResponseWriter, req *http.Request, name string, modtime time.Time, size int64,
	ctype string, provider LinkProvider, threads int, chunkBytes int64) {
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	if !modtime.IsZero() {
		w.Header().Set("Last-Modified", modtime.UTC().Format(http.TimeFormat))
	}
	if size < 0 {
		servePassthrough(w, req, provider)
		return
	}

	rg, hasRange, ok := parseRange(req.Header.Get("Range"), size)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(rg.length, 10))
	status := http.StatusOK
	if hasRange {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", rg.start, rg.start+rg.length-1, size))
	}
	if req.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	if rg.length == 0 { // 空文件
		w.WriteHeader(status)
		return
	}
	mr := NewMultiReader(req.Context(), provider, rg.start, rg.length, threads, chunkBytes)
	defer mr.Close()
	w.WriteHeader(status)
	io.Copy(w, mr) // 客户端断开→req.Context 取消→MultiReader 退出；此处无法再改状态码
}

// servePassthrough size 未知时的单流透传：转发客户端 Range，镜像上游响应。
func servePassthrough(w http.ResponseWriter, req *http.Request, provider LinkProvider) {
	url, hdr, err := provider(req.Context())
	if err != nil {
		http.Error(w, "获取直链失败", http.StatusBadGateway)
		return
	}
	up, err := http.NewRequestWithContext(req.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range hdr {
		for _, v := range vs {
			up.Header.Add(k, v)
		}
	}
	if r := req.Header.Get("Range"); r != "" {
		up.Header.Set("Range", r)
	}
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		http.Error(w, "拉取源失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if req.Method != http.MethodHead {
		io.Copy(w, resp.Body)
	}
}
