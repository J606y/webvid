package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"

	"newlist/internal/driver"
	"newlist/internal/stream"
	"newlist/internal/user"
)

// Progress 由任务层实现（task.Task 结构化满足），Transfer 通过它上报进度。
type Progress interface {
	SetTotal(n int64)
	SetFile(name string)
	Add(n int64)
}

// SameStorage 判断 src 与 dstDir 是否落在同一存储，并给出目标是否可上传。
// handler 据此决定：同存储走同步 MoveCopy、跨存储建任务、目标不可写直接拒绝。
func (f *FS) SameStorage(u *user.User, src, dstDir string) (same bool, dstUploadable bool, err error) {
	sm, _, err := f.Resolve(u, src)
	if err != nil {
		return false, false, err
	}
	dm, _, err := f.Resolve(u, dstDir)
	if err != nil {
		return false, false, err
	}
	_, up := dm.drv.(driver.Uploader)
	return sm.ID == dm.ID, up, nil
}

type fileJob struct {
	srcRel    string // 源条目相对路径
	dstDirRel string // 目标所在目录相对路径
	name      string
	size      int64
}

// Transfer 跨存储转存：流式拉源写目标，目录树先建目录再逐文件复制；
// isMove 时全部成功后删除源子树。进度经 pr 上报，ctx 取消即中止。
func (f *FS) Transfer(ctx context.Context, u *user.User, src, dstDir string, isMove bool, pr Progress) error {
	sm, srcRel, err := f.Resolve(u, src)
	if err != nil {
		return err
	}
	dm, dstRel, err := f.Resolve(u, dstDir)
	if err != nil {
		return err
	}
	dw, wok := dm.drv.(driver.Writer)
	up, uok := dm.drv.(driver.Uploader)
	if !wok || !uok {
		return errors.New("目标存储不支持写入")
	}

	sfi, err := sm.drv.Stat(ctx, srcRel)
	if err != nil {
		return err
	}
	base := path.Base(src)

	// 规划：files 待复制文件（含大小），dirs 目标待建目录（浅→深）
	var files []fileJob
	var dirs []string
	if sfi.IsDir {
		if err := f.planDir(ctx, sm, srcRel, joinRel(dstRel, base), &files, &dirs); err != nil {
			return err
		}
	} else {
		files = []fileJob{{srcRel: srcRel, dstDirRel: dstRel, name: base, size: sfi.Size}}
	}

	var total int64
	for _, fj := range files {
		total += fj.size
	}
	pr.SetTotal(total)

	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := dw.MakeDir(ctx, dir); err != nil && !errors.Is(err, driver.ErrExist) {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}

	for _, fj := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.copyOne(ctx, sm, up, fj, pr); err != nil {
			return err
		}
	}

	if isMove {
		sw, ok := sm.drv.(driver.Writer)
		if !ok {
			return errors.New("源存储不支持删除，已完成复制但源文件保留")
		}
		if err := sw.Remove(ctx, srcRel); err != nil {
			return fmt.Errorf("删除源失败（目标已复制成功）: %w", err)
		}
	}
	return nil
}

// planDir 递归枚举源目录：目标目录入 dirs（先父后子），文件入 files。
func (f *FS) planDir(ctx context.Context, sm *Mount, srcRel, dstRel string, files *[]fileJob, dirs *[]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*dirs = append(*dirs, dstRel)
	items, err := sm.drv.List(ctx, srcRel)
	if err != nil {
		return err
	}
	for _, it := range items {
		childSrc := joinRel(srcRel, it.Name)
		if it.IsDir {
			if err := f.planDir(ctx, sm, childSrc, joinRel(dstRel, it.Name), files, dirs); err != nil {
				return err
			}
		} else {
			*files = append(*files, fileJob{srcRel: childSrc, dstDirRel: dstRel, name: it.Name, size: it.Size})
		}
	}
	return nil
}

// copyOne 复制单个文件，文件级重试 2 次（共 3 次尝试）；失败重试前回退已计进度。
func (f *FS) copyOne(ctx context.Context, sm *Mount, up driver.Uploader, fj fileJob, pr Progress) error {
	pr.SetFile(fj.name)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cr := &countingReader{ctx: ctx, pr: pr}
		err := func() error {
			lk, err := sm.drv.Link(ctx, fj.srcRel)
			if err != nil {
				return err
			}
			var r io.ReadCloser
			switch {
			case lk.Local != nil:
				r = lk.Local
			case lk.URL != "":
				// 源为远端直链：够大且配置了多线程 → 并发 Range 分块拉源
				if opts := sm.accelOpts(); opts.Threads > 1 && fj.size > opts.ChunkBytes {
					lr := &LinkResult{Link: lk, Refresh: func(rctx context.Context) (*driver.Link, error) {
						return sm.drv.Link(rctx, fj.srcRel)
					}}
					r = stream.NewMultiReader(ctx, lr.Provider(), 0, fj.size, opts.Threads, opts.ChunkBytes)
					break
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, lk.URL, nil)
				if err != nil {
					return err
				}
				for k, vs := range lk.Header {
					for _, v := range vs {
						req.Header.Add(k, v)
					}
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				if resp.StatusCode >= 300 {
					resp.Body.Close()
					return fmt.Errorf("拉取源文件失败: HTTP %d", resp.StatusCode)
				}
				r = resp.Body
			default:
				return errors.New("源存储未返回可用的下载方式")
			}
			defer r.Close()
			var rr io.Reader = r
			if f.copyLim != nil { // 复制限速：所有转存任务共享全局速率
				rr = f.copyLim.Reader(ctx, rr)
			}
			cr.r = rr
			return up.Put(ctx, fj.dstDirRel, fj.name, cr, fj.size)
		}()
		if err == nil {
			return nil
		}
		lastErr = err
		pr.Add(-cr.n) // 回退本次已计字节
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("复制 %s 失败（已重试 2 次）: %w", fj.name, lastErr)
}

// countingReader 包装源读取流，把读到的字节数上报进度；
// 每次 Read 前检查 ctx——本地文件句柄不走 HTTP，不查 ctx 的话取消无法中断 io.Copy。
type countingReader struct {
	ctx context.Context
	r   io.Reader
	pr  Progress
	n   int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		c.pr.Add(int64(n))
	}
	return n, err
}

// joinRel 拼相对路径（"" 为存储根）。
func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
