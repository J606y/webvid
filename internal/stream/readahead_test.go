package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// countingReader 记录源侧被读走了多少字节，用于验证「读取方不读，预读仍在收」。
type countingReader struct {
	r      io.Reader
	read   atomic.Int64
	closed atomic.Bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *countingReader) Close() error {
	c.closed.Store(true)
	return nil
}

// TestReadAheadPassthrough 预读不改变内容，也不改变 EOF 语义。
func TestReadAheadPassthrough(t *testing.T) {
	want := bytes.Repeat([]byte("webvid"), 40000) // 240KB，跨多格
	src := &countingReader{r: bytes.NewReader(want)}
	r := newReadAhead(context.Background(), src, 4, 1<<10)
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取出错: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("内容不一致: 读到 %d 字节，应为 %d", len(got), len(want))
	}
}

// TestReadAheadDecouples 读取方一个字节都不读时，预读也应把源拉到缓冲上限。
// 这正是它存在的理由：ffmpeg 停下来编码的那段时间，云盘那条连接不能跟着停。
func TestReadAheadDecouples(t *testing.T) {
	const block, blocks = 1 << 10, 8
	src := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{7}, 1<<20))}
	r := newReadAhead(context.Background(), src, blocks, block)
	defer r.Close()

	deadline := time.Now().Add(2 * time.Second)
	for src.read.Load() < int64(block*blocks) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := src.read.Load(); n < int64(block*blocks) {
		t.Fatalf("读取方未读时预读应已填满缓冲，实际只从源取了 %d 字节", n)
	}
}

// TestReadAheadError 源出错时错误原样交给读取方，不被吞成 EOF。
func TestReadAheadError(t *testing.T) {
	boom := errors.New("上游断了")
	src := &countingReader{r: io.MultiReader(bytes.NewReader([]byte("头几个字节")), errReader{boom})}
	r := newReadAhead(context.Background(), src, 4, 8)
	defer r.Close()

	if _, err := io.ReadAll(r); !errors.Is(err, boom) {
		t.Fatalf("应原样返回源的错误, got %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// TestReadAheadClose Close 幂等，且关掉源。
func TestReadAheadClose(t *testing.T) {
	src := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{1}, 1<<20))}
	r := newReadAhead(context.Background(), src, 4, 1<<10)
	r.Close()
	r.Close()
	if !src.closed.Load() {
		t.Fatal("Close 应关掉源")
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("关闭后继续读应报错")
	}
}
