package stream

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// 块 0 的流式直通：上游响应一到就往客户端转发，不等整块下满。
//
// 起因是实测：浏览器播 moov 在文件尾（非 faststart）的 mp4 时，为找索引会发出几十条
// 零碎的 Range 请求，每条只取几十 KB。若首块必须下满 512KB 才吐第一个字节，每条请求都要
// 先付一次「建连 + 下满整块」——Google Drive 跨国实测 0.86 秒，其中约 0.47 秒纯粹是在等
// 那 512KB 传完。几十条叠起来就是半分钟白屏，而客户端要的可能只有 32KB。
//
// 只有块 0 走这条路。块 1 及之后本就在并发预取、读到时已在内存里，没有等待可省；
// 把它们也流式化只会破坏「按序拼接」这个前提。
//
// 代价是流一旦开始就无法整体重发（字节已经出门）。故中断后带偏移续拉，见 resume。

// headResumes 首块流中断后的续拉次数上限。
// 次数耗尽即让响应短传——这是 HTTP 的正常降级，客户端会自己按 Content-Range 续传，
// 比死等或伪造数据都干净。
const headResumes = 4

// headStream 块 0 的流式读取器。非并发安全（单读者），但 Close 可与 Read 并发。
type headStream struct {
	m     *MultiReader
	start int64 // 块在源文件中的起点
	size  int64 // 块总字节数
	pos   int64 // 已向客户端输出的字节数

	// mu 保护在途上游流三件套与 closed；Read 不在持锁期间阻塞读取，
	// 否则 Close 要等到上游吐字节才拿得到锁。
	mu      sync.Mutex
	body    io.ReadCloser
	stall   *stallReader
	cancel  context.CancelFunc
	closed  bool
	resumed int
}

func newHeadStream(m *MultiReader) *headStream {
	start, size := m.plan.at(0)
	return &headStream{m: m, start: m.offset + start, size: size}
}

// Read 吐出块 0 的下一段字节。上游中断时按 resume 的策略续拉，对调用方透明；
// 块 0 出完返回 io.EOF——调用方据此切到缓冲队列。
func (h *headStream) Read(p []byte) (int, error) {
	if h.pos >= h.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if rest := h.size - h.pos; int64(len(p)) > rest {
		p = p[:rest] // 上游多给的字节一律不要，越界即数据损坏
	}
	for {
		r, err := h.ensure()
		if err != nil {
			return 0, err
		}
		n, err := r.Read(p)
		if n > 0 {
			h.pos += int64(n)
			return n, nil
		}
		if err == nil {
			continue // 合法的空读
		}
		if h.pos >= h.size {
			return 0, io.EOF
		}
		if rerr := h.resume(err); rerr != nil {
			return 0, rerr
		}
	}
}

// ensure 返回在途的上游流；尚未打开或刚被中断则重新打开。
func (h *headStream) ensure() (io.Reader, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, context.Canceled
	}
	if h.stall != nil {
		r := h.stall
		h.mu.Unlock()
		return r, nil
	}
	h.mu.Unlock()
	return h.open()
}

// open 打开 [start+pos, start+size-1] 的上游流。首字节尚未出门，故可照常换链/退避重试。
// 刻意不设总时限：流式转发的时长取决于客户端消费速度，整块那套 chunkDeadline 在这里
// 会把慢速播放的客户端一并掐死；挂住由 stallReader 负责发现。
func (h *headStream) open() (io.Reader, error) {
	type opened struct {
		body   io.ReadCloser
		stall  *stallReader
		cancel context.CancelFunc
	}
	// 续拉时已经吐过字节，源若回 200 全量会从文件头重来，只能在 pos==0 时接受
	acceptFull := h.pos == 0 && h.m.chunks == 1 && h.m.offset == 0
	end := h.start + h.size - 1
	got, err := openWithRetry(h.m, func(url string, hdr http.Header) (opened, bool, time.Duration, error) {
		ctx, cancel := context.WithCancel(h.m.ctx)
		body, refresh, wait, err := h.m.openRange(ctx, url, hdr, h.start+h.pos, end, acceptFull)
		if err != nil {
			cancel()
			return opened{}, refresh, wait, err
		}
		return opened{body: body, stall: newStallReader(body, stallTimeout, cancel), cancel: cancel}, false, 0, nil
	})
	if err != nil {
		if h.m.ctx.Err() == nil {
			err = fmt.Errorf("首块 [%d-%d] 打开失败: %w", h.start+h.pos, end, err)
			log.Printf("[stream] %v", err)
		}
		return nil, err
	}
	h.mu.Lock()
	if h.closed { // 打开期间被关掉，接手的流要就地释放，否则连接泄漏
		h.mu.Unlock()
		got.stall.stop()
		got.cancel()
		got.body.Close()
		return nil, context.Canceled
	}
	h.body, h.stall, h.cancel = got.body, got.stall, got.cancel
	h.mu.Unlock()
	return got.stall, nil
}

// resume 上游中断后准备续拉：丢掉旧流，下一轮 ensure 会带着已读偏移重开。
// 返回非 nil 表示放弃——响应就此短传，由客户端按 HTTP 语义自行续传。
func (h *headStream) resume(cause error) error {
	h.release()
	if err := h.m.ctx.Err(); err != nil {
		return err
	}
	h.resumed++
	if h.resumed > headResumes {
		err := fmt.Errorf("首块流中断，续拉 %d 次未果（已输出 %d/%d 字节）: %w",
			headResumes, h.pos, h.size, cause)
		log.Printf("[stream] %v", err)
		return err
	}
	if !h.m.pause(retryBackoff * time.Duration(h.resumed)) {
		return h.m.ctx.Err()
	}
	return nil
}

// release 关掉在途上游流并清空三件套；无在途流时是空操作。
func (h *headStream) release() {
	h.mu.Lock()
	body, stall, cancel := h.body, h.stall, h.cancel
	h.body, h.stall, h.cancel = nil, nil, nil
	h.mu.Unlock()
	if stall != nil {
		stall.stop()
	}
	if cancel != nil {
		cancel()
	}
	if body != nil {
		body.Close()
	}
}

// Close 幂等：中断在途请求并释放连接。可与 Read 并发调用。
func (h *headStream) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.release()
	return nil
}
