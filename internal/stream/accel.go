// Package stream 提供远端直链的并发 Range 分块下载（多线程加速）与代理响应组装。
// 纯标准库实现，不依赖项目内其他包，便于独立单测。
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LinkProvider 返回当前可用的直链（URL + 请求头，如 PikPak 必须绑定的 UA）。
// 首个分块前惰性调用一次并缓存；分块遇过期状态码（401/403/404/410）会强制重取。
type LinkProvider func(ctx context.Context) (url string, header http.Header, err error)

// 可调常量（编译期固定，够用即可，不进配置）。
const (
	chunkAttempts = 4               // 每块硬错误最多尝试次数
	retryBackoff  = 200 * time.Millisecond // 硬错误重试间隔基数（×attempt）
	chunkTimeout  = 2 * time.Minute // 单块请求超时（防一块卡死堵住整个窗口）

	// 云盘按请求频率限流（OneDrive/SharePoint 429/503 + Retry-After 常达数十秒）。
	// 限流等待不消耗尝试次数，只受累计预算约束——否则一个限流窗口就掐死整条流，
	// 播放器重试又加剧限流，表现为"一直重连播放不出来"。
	throttleBudget  = 2 * time.Minute  // 单块累计限流等待预算
	throttleDefault = 2 * time.Second  // 无 Retry-After 头时的等待
	throttleMax     = 30 * time.Second // 单次等待上限（防恶意/异常头长挂）
)

// retryAfter 解析 Retry-After 秒数，钳制到 [throttleDefault, throttleMax]。
func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n > 0 {
		return min(time.Duration(n)*time.Second, throttleMax)
	}
	return throttleDefault
}

var errNoRange = errors.New("源不支持 Range 分块")

// chunkResult 一个分块的下载结果。
type chunkResult struct {
	buf []byte
	err error
}

// MultiReader 并发取 Range 分块、滑动窗口按序输出的 io.ReadCloser。
type MultiReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *http.Client

	provider LinkProvider
	offset   int64
	length   int64
	chunk    int64
	chunks   int
	window   int

	// 直链缓存（换链单飞：worker 带着自己用过的 gen 来刷，gen 未变才真调 provider）
	linkMu   sync.Mutex
	linkURL  string
	linkHdr  http.Header
	linkGen  int
	linkInit bool

	mu       sync.Mutex
	cond     *sync.Cond
	results  map[int]chunkResult
	nextJob  int // 下一个待领取的块序号
	nextRead int // 读端正在等待的块序号
	cur      []byte
	readErr  error
	closed   bool
	wg       sync.WaitGroup
}

// NewMultiReader 输出直链内容的 [offset, offset+length) 区间，共 length 字节（必须 >0）。
// threads 个 worker 并发取块，窗口=threads 限制内存 ≈ (threads+1)×chunkBytes。
// 返回的 Reader 非并发安全（单读者）；Close 幂等，取消所有在途请求并等 worker 退出。
func NewMultiReader(ctx context.Context, provider LinkProvider, offset, length int64, threads int, chunkBytes int64) io.ReadCloser {
	if threads < 1 {
		threads = 1
	}
	if chunkBytes < 64*1024 {
		chunkBytes = 64 * 1024
	}
	cctx, cancel := context.WithCancel(ctx)
	chunks := int((length + chunkBytes - 1) / chunkBytes)
	m := &MultiReader{
		ctx:      cctx,
		cancel:   cancel,
		client:   http.DefaultClient,
		provider: provider,
		offset:   offset,
		length:   length,
		chunk:    chunkBytes,
		chunks:   chunks,
		window:   threads,
		results:  map[int]chunkResult{},
	}
	m.cond = sync.NewCond(&m.mu)
	if length <= 0 {
		m.readErr = io.EOF
		return m
	}
	if threads > chunks {
		threads = chunks
	}
	m.wg.Add(threads)
	for i := 0; i < threads; i++ {
		go m.worker()
	}
	// ctx 取消时唤醒所有等待者（Read 阻塞在 Cond 上感知不到 ctx）
	go func() {
		<-cctx.Done()
		m.cond.Broadcast()
	}()
	return m
}

// worker 循环领块→下载→投递，直到块发完或 ctx 取消。
func (m *MultiReader) worker() {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		for m.nextJob < m.chunks && m.nextJob >= m.nextRead+m.window && m.ctx.Err() == nil {
			m.cond.Wait() // 窗口满，等读端消费
		}
		if m.nextJob >= m.chunks || m.ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		idx := m.nextJob
		m.nextJob++
		m.mu.Unlock()

		buf, err := m.fetchChunk(idx)

		m.mu.Lock()
		m.results[idx] = chunkResult{buf: buf, err: err}
		m.cond.Broadcast()
		abort := err != nil
		m.mu.Unlock()
		if abort {
			return // 一块彻底失败即整体失败，无需继续
		}
	}
}

// chunkRange 第 idx 块在源文件中的 [start, end]（end 含）。
func (m *MultiReader) chunkRange(idx int) (start, end int64) {
	start = m.offset + int64(idx)*m.chunk
	end = start + m.chunk - 1
	if last := m.offset + m.length - 1; end > last {
		end = last
	}
	return
}

// getLink 取当前直链；usedGen==当前 gen 且 force 时才真调 provider（换链单飞）。
func (m *MultiReader) getLink(usedGen int, force bool) (string, http.Header, int, error) {
	m.linkMu.Lock()
	defer m.linkMu.Unlock()
	if !m.linkInit || (force && usedGen == m.linkGen) {
		u, h, err := m.provider(m.ctx)
		if err != nil {
			return "", nil, m.linkGen, err
		}
		m.linkURL, m.linkHdr = u, h
		m.linkGen++
		m.linkInit = true
	}
	return m.linkURL, m.linkHdr, m.linkGen, nil
}

// pause ctx 感知的睡眠；返回 false 表示 ctx 已取消。
func (m *MultiReader) pause(d time.Duration) bool {
	select {
	case <-m.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// fetchChunk 下载一个分块：硬错误带退避重试、直链过期换链、限流按 Retry-After 等待。
func (m *MultiReader) fetchChunk(idx int) ([]byte, error) {
	start, end := m.chunkRange(idx)
	size := end - start + 1
	var lastErr error
	gen := 0
	refresh := false
	var throttled time.Duration
	for attempt := 1; attempt <= chunkAttempts; {
		url, hdr, g, err := m.getLink(gen, refresh)
		if err != nil {
			lastErr = fmt.Errorf("获取直链失败: %w", err)
			refresh = false
			attempt++
			if attempt <= chunkAttempts && !m.pause(retryBackoff*time.Duration(attempt-1)) {
				return nil, m.ctx.Err()
			}
			continue
		}
		gen, refresh = g, false
		buf, retryRefresh, wait, err := m.doRange(url, hdr, start, end, size, idx)
		if err == nil {
			return buf, nil
		}
		lastErr = err
		refresh = retryRefresh
		if m.ctx.Err() != nil {
			return nil, m.ctx.Err()
		}
		if errors.Is(err, errNoRange) {
			return nil, err // 源不支持 Range，重试无意义
		}
		if wait > 0 && throttled+wait <= throttleBudget {
			throttled += wait // 限流等待不消耗尝试次数，流照常存活（这段时间无新数据而已）
			if !m.pause(wait) {
				return nil, m.ctx.Err()
			}
			continue
		}
		attempt++
		if attempt <= chunkAttempts && !m.pause(retryBackoff*time.Duration(attempt-1)) {
			return nil, m.ctx.Err()
		}
	}
	err := fmt.Errorf("分块 %d [%d-%d] 下载失败（已重试）: %w", idx, start, end, lastErr)
	log.Printf("[stream] %v", err)
	return nil, err
}

// doRange 发一次 Range 请求读满分块；返回 (数据, 是否应换链重试, 限流等待时长, 错误)。
func (m *MultiReader) doRange(url string, hdr http.Header, start, end, size int64, idx int) ([]byte, bool, time.Duration, error) {
	rctx, cancel := context.WithTimeout(m.ctx, chunkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, 0, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, false, 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		buf := make([]byte, size)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			return nil, false, 0, fmt.Errorf("分块读取中断: %w", err)
		}
		return buf, false, 0, nil
	case http.StatusOK:
		// 服务器不认 Range：仅当整个请求区间就是文件开头的唯一一块时可接受
		if idx == 0 && m.chunks == 1 && m.offset == 0 {
			buf := make([]byte, size)
			if _, err := io.ReadFull(resp.Body, buf); err != nil {
				return nil, false, 0, fmt.Errorf("读取源失败: %w", err)
			}
			return buf, false, 0, nil
		}
		return nil, false, 0, errNoRange
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return nil, true, 0, fmt.Errorf("直链疑似过期: HTTP %d", resp.StatusCode)
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return nil, false, retryAfter(resp.Header.Get("Retry-After")),
			fmt.Errorf("源限流: HTTP %d", resp.StatusCode)
	default:
		return nil, false, 0, fmt.Errorf("拉取分块失败: HTTP %d", resp.StatusCode)
	}
}

// Read 按序输出分块内容；某块彻底失败后恒返回该错误。
func (m *MultiReader) Read(p []byte) (int, error) {
	if len(m.cur) == 0 {
		m.mu.Lock()
		for {
			if m.readErr != nil {
				m.mu.Unlock()
				return 0, m.readErr
			}
			if err := m.ctx.Err(); err != nil {
				m.mu.Unlock()
				return 0, err
			}
			if m.nextRead >= m.chunks {
				m.readErr = io.EOF
				m.mu.Unlock()
				return 0, io.EOF
			}
			if r, ok := m.results[m.nextRead]; ok {
				delete(m.results, m.nextRead)
				if r.err != nil {
					m.readErr = r.err
					m.mu.Unlock()
					return 0, r.err
				}
				m.cur = r.buf
				m.nextRead++
				m.cond.Broadcast() // 窗口前移，放行 worker
				break
			}
			m.cond.Wait()
		}
		m.mu.Unlock()
	}
	n := copy(p, m.cur)
	m.cur = m.cur[n:]
	return n, nil
}

// Close 幂等：取消在途请求、唤醒阻塞的 Read、等全部 worker 退出。
func (m *MultiReader) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.cond.Broadcast()
	m.wg.Wait()
	return nil
}
