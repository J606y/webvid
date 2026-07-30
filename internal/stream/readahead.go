package stream

import (
	"context"
	"errors"
	"io"
	"sync"
)

// 预读缓冲的尺寸：32 块 × 512KB = 16MB。块按需分配，只读几百 KB 的小请求
// （ffprobe 探元数据）不会因此占住整段内存。
const (
	readaheadBlock  = 512 << 10
	readaheadBlocks = 32
)

// block 是预读队列里的一格：要么是数据，要么是终止原因，二者不并存。
type block struct {
	buf []byte
	err error
}

// readAhead 把上游连接与读取方解耦：一条 goroutine 专职把字节从连接搬进内存队列，
// 读取方慢下来也不打断它继续收。
//
// 服务器内部的读取方（ffmpeg / ffprobe）是走走停停的：编码、写盘、等分片轮转，每一
// 下都让读端停住。没有这层缓冲时，这个停顿会顺着回环一路顶回云盘那条 TCP 的接收
// 窗口，跨国链路的拥塞窗口随之塌陷，ffmpeg 回来时带宽得重新爬坡——表现就是「拉流
// 不积极、卡了很久后台却看不到网络活动」。有了它，连接上始终有人在收，ffmpeg 的停顿
// 由这段内存吸收。
//
// 只读不写，单读者；Close 幂等，并负责关掉 src（所有权移交给本对象）。
type readAhead struct {
	ctx    context.Context
	cancel context.CancelFunc
	src    io.ReadCloser
	ch     chan block

	cur []byte // 当前正在吐出的那一格剩余部分
	err error  // 终止原因，一旦落定后续 Read 恒返回它

	once sync.Once
}

// newReadAhead 接管 src 并立即开始预读。blocks/size 决定缓冲上限（blocks × size 字节）。
func newReadAhead(ctx context.Context, src io.ReadCloser, blocks, size int) io.ReadCloser {
	cctx, cancel := context.WithCancel(ctx)
	r := &readAhead{ctx: cctx, cancel: cancel, src: src, ch: make(chan block, blocks)}
	go r.pump(size)
	return r
}

// pump 循环读满一格投进队列，直到源结束、出错或本对象被关掉。
func (r *readAhead) pump(size int) {
	defer close(r.ch)
	for {
		buf := make([]byte, size)
		n, err := io.ReadFull(r.src, buf)
		if n > 0 && !r.put(block{buf: buf[:n]}) {
			return
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			r.put(block{err: io.EOF}) // 读满即止，正常收尾
		default:
			r.put(block{err: err})
		}
		return
	}
}

// put 投递一格；本对象已关闭（ctx 取消）时返回 false，pump 据此收工。
func (r *readAhead) put(b block) bool {
	select {
	case r.ch <- b:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *readAhead) Read(p []byte) (int, error) {
	for len(r.cur) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		b, ok := <-r.ch
		if !ok {
			// pump 没留话就退了 = 本对象被关掉或上游 ctx 取消
			if r.err = r.ctx.Err(); r.err == nil {
				r.err = io.ErrUnexpectedEOF
			}
			return 0, r.err
		}
		if b.err != nil {
			r.err = b.err
			return 0, r.err
		}
		r.cur = b.buf
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	return n, nil
}

// Close 停止预读并关掉源。关 src 是为了让阻塞在读上的 pump 立刻脱身——
// 光取消 ctx 拦不住一个已经进到 Read 里的调用。
func (r *readAhead) Close() error {
	var err error
	r.once.Do(func() {
		r.cancel()
		err = r.src.Close()
	})
	return err
}
