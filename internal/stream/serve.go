package stream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
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
//
// 客户端那一侧永远是单条有序响应——浏览器对一个文件只发一个 Range 请求，HTTP 语义
// 决定了它不能拆到多条连接上发。并发只发生在服务器↔云盘这一段（见 MultiReader）。
func Serve(w http.ResponseWriter, req *http.Request, name string, modtime time.Time, size int64,
	ctype string, provider LinkProvider, o Opts) {
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
	if o.Label == "" {
		o.Label = name
	}
	mr := NewMultiReader(req.Context(), provider, rg.start, rg.length, o)
	defer mr.Close()
	// 第 0 块到手之前不提交状态码。一旦 WriteHeader 出去就再也改不回来了，上游此后
	// 无论怎么失败（限流、配额耗尽、令牌失效、超时），客户端看到的都是同一件事——
	// 一个承诺了 Content-Length 却提前断掉的 206，播放器只能报"网络错误"。
	// 服务端明明知道真正的原因，却没有任何位置说得出口，这正是这类故障最难定位的地方。
	// 渐进分块已把首块压到 512KB（见 rampBase），这点等待换的是一个说得清的失败。
	var head [1]byte
	n, err := io.ReadFull(mr, head[:])
	if err != nil {
		if req.Context().Err() != nil {
			return // 客户端自己走了，不是故障
		}
		log.Printf("[stream] 首块打开失败 %s: %v", name, err)
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Range")
		http.Error(w, openFailMessage(err), http.StatusBadGateway)
		return
	}
	w.WriteHeader(status)
	if _, err := w.Write(head[:n]); err != nil {
		return
	}
	io.Copy(w, mr) // 客户端断开→req.Context 取消→MultiReader 退出；此处无法再改状态码
}

// openFailMessage 把首字节前的失败翻译成一句给用户看的话。
// 本包不依赖项目内其他包（见包注释），故不走 util.Humanize——它按关键词匹配，
// 会把限流的 403 一律说成"没有权限"，那比不说更误导。
func openFailMessage(err error) string {
	switch {
	case errors.Is(err, errThrottled):
		return "存储正在限制访问频率，请稍后重试"
	case errors.Is(err, errRelink):
		return "存储拒绝了这次访问，可能需要在后台重新授权"
	case errors.Is(err, errNoRange):
		return "该存储不支持分段读取，无法播放"
	case errors.Is(err, context.DeadlineExceeded):
		return "连接存储超时，请稍后重试"
	}
	return "无法从存储读取该文件，请稍后重试"
}

// ServeSingle 单连接透传：整个响应只向源发一个请求，Range 语义与 Serve 一致。
// 供服务器内部读取方（ffmpeg/ffprobe 转码、探测、抽帧）使用——它们顺序读且自带
// 断线续传（-reconnect），单连接远比分块并发温和：分块模式整读一个大文件会产生
// "每块一请求"的风暴（4GB/4MB≈千次），OneDrive 等云盘按请求频率限流（429/503），
// 一旦触发整流即断、重试又加剧限流，表现为播放器"一直重连"。
// 直链过期换链与限流等待都发生在首字节之前；响应已开始后断流由调用方续传。
func ServeSingle(w http.ResponseWriter, req *http.Request, name string, modtime time.Time, size int64,
	ctype string, provider LinkProvider) {
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
	if req.Method == http.MethodHead || rg.length == 0 {
		w.WriteHeader(status)
		return
	}
	body, err := openUpstream(req.Context(), provider, rg, size)
	if err != nil {
		log.Printf("[stream] 单流打开失败 %s: %v", name, err)
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Range")
		http.Error(w, openFailMessage(err), http.StatusBadGateway)
		return
	}
	// 套一层预读：内部读取方（ffmpeg/ffprobe）是走走停停的，不缓冲的话它每停一下
	// 都会顶回云盘那条 TCP 的接收窗口，跨国链路上的带宽因此反复塌陷再爬坡。见 readAhead。
	src := newReadAhead(req.Context(), body, readaheadBlocks, readaheadBlock)
	defer src.Close() // 所有权已交给它，由它关掉 body
	w.WriteHeader(status)
	io.Copy(w, src) // 客户端断开或源断流即止；源断流表现为短传，读取方自行续传
}

// 单流打开重试参数：仅作用于首字节前（响应开始后无法重来）。
const (
	openAttempts      = 4
	openThrottleLimit = time.Minute
)

// singleClient 带响应头超时（体传输不限时——单流本就长寿命）。
// 同样禁 h2：h2 的每流流控窗口固定 4MB，长跑的整片顺序读在高时延链路上会被它封顶，
// 而单流本就只用一条连接，h2 的多路复用在这里一点好处也没有。理由详见 chunkClient。
var singleClient = &http.Client{Transport: func() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t2 := t.Clone()
	t2.ForceAttemptHTTP2 = false
	t2.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	t2.ResponseHeaderTimeout = 30 * time.Second
	return t2
}()}

// openUpstream 打开源的 [start, start+length) 单流：
// 401/403/404/410 → 换链重试（provider 再调用即强制重取直链）；
// 429/503 → 按 Retry-After 等待后重试（累计预算内不消耗尝试次数）；
// 换链之后仍被拒 → 按限流处置（见 escalate，分块路径同此判定）。
func openUpstream(ctx context.Context, provider LinkProvider, rg httpRange, size int64) (io.ReadCloser, error) {
	var lastErr error
	var url string
	var hdr http.Header
	var throttled time.Duration
	throttleN := 0
	relinked := false
	for attempt := 1; attempt <= openAttempts; {
		if url == "" {
			u, h, err := provider(ctx)
			if err != nil {
				return nil, fmt.Errorf("获取直链失败: %w", err)
			}
			url, hdr = u, h
		}
		up, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		for k, vs := range hdr {
			for _, v := range vs {
				up.Header.Add(k, v)
			}
		}
		up.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rg.start, rg.start+rg.length-1))
		resp, err := singleClient.Do(up)
		if err != nil {
			lastErr = err
			attempt++
			if !ctxPause(ctx, retryBackoff*time.Duration(attempt-1)) {
				return nil, ctx.Err()
			}
			continue
		}
		switch resp.StatusCode {
		case http.StatusPartialContent:
			return resp.Body, nil
		case http.StatusOK:
			// 源不认 Range：仅当请求区间就是全文件时可接受（截到应答长度防超写）
			if rg.start == 0 && rg.length == size {
				return struct {
					io.Reader
					io.Closer
				}{io.LimitReader(resp.Body, rg.length), resp.Body}, nil
			}
			resp.Body.Close()
			return nil, errNoRange
		}
		note := upstreamNote(resp.Body)
		resp.Body.Close()
		switch escalate(classifyErrStatus(resp.StatusCode), relinked) {
		case dispRelink:
			lastErr = fmt.Errorf("%w: HTTP %d%s", errRelink, resp.StatusCode, note)
			url = "" // 下轮换新链
			relinked = true
			attempt++
		case dispThrottle:
			throttleN++
			wait := throttleWait(resp.Header.Get("Retry-After"), throttleN)
			lastErr = fmt.Errorf("%w: HTTP %d%s", errThrottled, resp.StatusCode, note)
			if throttled+wait <= openThrottleLimit {
				throttled += wait
				if !ctxPause(ctx, wait) {
					return nil, ctx.Err()
				}
				continue // 限流等待不消耗尝试次数
			}
			attempt++
		default: // dispHard
			lastErr = fmt.Errorf("拉取源失败: HTTP %d%s", resp.StatusCode, note)
			attempt++
			if attempt <= openAttempts && !ctxPause(ctx, retryBackoff*time.Duration(attempt-1)) {
				return nil, ctx.Err()
			}
		}
	}
	return nil, lastErr
}

func ctxPause(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
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
	resp, err := singleClient.Do(up) // 同为单流，复用其响应头超时与 h1.1 设定
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
