package telegram

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// chunkSize upload.getFile 单块大小：满足 offset/limit 4096 对齐、
// limit 整除 1MiB、块不跨 1MiB 边界的全部服务端约束。
const chunkSize = 512 << 10

// fetchFunc 拉取自 offset 起的一块（offset 恒为 chunkSize 对齐，末块可短）。
type fetchFunc func(ctx context.Context, offset int64) ([]byte, error)

// tgReader 把 MTProto 分块下载适配成 io.ReadSeekCloser：
// http.ServeContent（Range 播放）与跨存储转存共用；Seek 纯指针运算不打网络。
type tgReader struct {
	ctx    context.Context
	fetch  fetchFunc
	size   int64
	pos    int64
	buf    []byte
	bufOff int64 // buf[0] 的文件偏移；<0 表示无缓冲
}

func newReader(ctx context.Context, fetch fetchFunc, size int64) *tgReader {
	return &tgReader{ctx: ctx, fetch: fetch, size: size, bufOff: -1}
}

func (r *tgReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.bufOff < 0 || r.pos < r.bufOff || r.pos >= r.bufOff+int64(len(r.buf)) {
		off := r.pos - r.pos%chunkSize
		b, err := r.fetch(r.ctx, off)
		if err != nil {
			return 0, err
		}
		if len(b) == 0 {
			return 0, io.ErrUnexpectedEOF
		}
		r.buf, r.bufOff = b, off
	}
	n := copy(p, r.buf[r.pos-r.bufOff:])
	if n == 0 { // 服务器返回的块比预期短，pos 落在空洞里
		return 0, io.ErrUnexpectedEOF
	}
	r.pos += int64(n)
	return n, nil
}

func (r *tgReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, errors.New("telegram: 非法 whence")
	}
	if abs < 0 {
		return 0, errors.New("telegram: 偏移为负")
	}
	r.pos = abs
	return abs, nil
}

func (r *tgReader) Close() error { return nil }

// docSource 负责真实取块：跟随 FILE_MIGRATE 换 DC、file_reference 过期时
// 重取消息刷新后重试。getFile/refresh 为函数字段，单测可注入假实现。
type docSource struct {
	mu  sync.Mutex
	loc *tg.InputDocumentFileLocation
	dc  int

	getFile func(ctx context.Context, dc int, loc *tg.InputDocumentFileLocation, offset int64) ([]byte, error)
	refresh func(ctx context.Context) (*tg.InputDocumentFileLocation, int, error)
}

func (s *docSource) fetch(ctx context.Context, offset int64) ([]byte, error) {
	s.mu.Lock()
	loc, dc := s.loc, s.dc
	s.mu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		b, err := s.getFile(ctx, dc, loc, offset)
		if err == nil {
			return b, nil
		}
		lastErr = err
		if e, ok := tgerr.AsType(err, "FILE_MIGRATE"); ok {
			dc = e.Argument
			s.mu.Lock()
			s.dc = dc
			s.mu.Unlock()
			continue
		}
		if isFileRefErr(err) {
			nloc, ndc, rerr := s.refresh(ctx)
			if rerr != nil {
				return nil, rerr
			}
			loc = nloc
			if ndc > 0 {
				dc = ndc
			}
			s.mu.Lock()
			s.loc, s.dc = loc, dc
			s.mu.Unlock()
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

// isFileRefErr 判定 file_reference 失效（FILE_REFERENCE_EXPIRED 及同族变体）。
func isFileRefErr(err error) bool {
	if e, ok := tgerr.As(err); ok {
		return strings.HasPrefix(e.Type, "FILE_REFERENCE")
	}
	return false
}
