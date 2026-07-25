package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"time"

	"golang.org/x/sync/errgroup"

	"newlist/internal/driver"
	"newlist/internal/stream"
	"newlist/internal/user"
	"newlist/internal/util"
)

// Progress 由任务层实现（task.Task 结构化满足），Transfer 通过它上报进度。
// FileStart/FileDone 成对上报而非「设置当前文件」：文件级并发时多个文件同时在途，
// 单值语义会被后开始的文件不断顶掉，展示就在几个名字之间跳。
type Progress interface {
	SetTotal(n int64)
	FileStart(name string)
	FileDone(name string)
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
		if err := f.planDir(ctx, sm, srcRel, util.JoinRel(dstRel, base), &files, &dirs); err != nil {
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

	// 目标已有文件快照，供下面的断点续传判断走内存而不是逐文件 Stat。
	// 逐文件 Stat 在云盘上极贵：googledrive/pikpak 的路径解析只缓存「找得到」的路径，
	// 目标缺文件时每次 Stat 都把整个目录重列一遍（大目录还要分页）；onedrive 则是每文件
	// 一次 Graph 请求。改成每个目标目录只列一次，请求数从「文件数」降到「目录数」。
	// 单文件转存不预扫：一次 Stat 就够，去列一个可能很大的目标目录反而更亏。
	existing, scanned := map[string]int64{}, map[string]bool{}
	if len(files) > 1 {
		for _, dir := range dstDirsOf(files) {
			if err := ctx.Err(); err != nil {
				return err
			}
			items, err := dm.drv.List(ctx, dir)
			if err != nil {
				continue // 列不出来（无权限等）→ 该目录退回逐文件 Stat，只慢不错
			}
			scanned[dir] = true
			for _, it := range items {
				if !it.IsDir {
					existing[util.JoinRel(dir, it.Name)] = it.Size
				}
			}
		}
	}

	// 文件级并发：单个转存任务内同时复制多个文件，并发度由 copy_file_workers 控制
	//（未设置=1=串行）。errgroup 首个非 nil 错误即取消 gctx，其余在途文件尽快返回，
	// 整任务失败——与原串行「首错即返回」语义一致（移动因此不删源）。
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(f.copyFileWorkersOrOne())
	for _, fj := range files {
		fj := fj
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			// 断点续传：目标已存在且大小一致 → 跳过，避免重试时重复下载/上传已完成文件
			//（尤其云盘有每日上传上限，如 Google Drive 750GB/天，重传已完成部分很浪费）。
			// 上传失败不会留下"半个可见文件"（resumable 会话未完成不出文件、小文件 multipart 原子），
			// 故同名同大小即视为已完成，安全。existing/scanned 在起 goroutine 前建好，只读。
			target := util.JoinRel(fj.dstDirRel, fj.name)
			if fj.size > 0 {
				if scanned[fj.dstDirRel] {
					if sz, ok := existing[target]; ok && sz == fj.size {
						pr.Add(fj.size)
						return nil
					}
				} else if fi, err := dm.drv.Stat(gctx, target); err == nil && !fi.IsDir && fi.Size == fj.size {
					pr.Add(fj.size)
					return nil
				}
			}
			return f.copyOne(gctx, sm, up, fj, pr)
		})
	}
	if err := g.Wait(); err != nil {
		return err
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

// dstDirsOf 收集 files 落地的目标目录，去重且保持首次出现的顺序。
// 只取真正有文件要放的目录——空目录列了也没用，白费一次请求。
func dstDirsOf(files []fileJob) []string {
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	for _, fj := range files {
		if !seen[fj.dstDirRel] {
			seen[fj.dstDirRel] = true
			out = append(out, fj.dstDirRel)
		}
	}
	return out
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
		childSrc := util.JoinRel(srcRel, it.Name)
		if it.IsDir {
			if err := f.planDir(ctx, sm, childSrc, util.JoinRel(dstRel, it.Name), files, dirs); err != nil {
				return err
			}
		} else {
			*files = append(*files, fileJob{srcRel: childSrc, dstDirRel: dstRel, name: it.Name, size: it.Size})
		}
	}
	return nil
}

// 失败归因：用户至少要知道是源端读不出来，还是目标端写不进去。
const (
	stageSource = "读取源文件"
	stageDest   = "写入目标"
)

// copyRetryWait 决定第 attempt 次失败后等多久再重试。
// 限流要按限流的节奏等——驱动层虽已就地按 Retry-After 退避过，这里仍要再让一手，
// 否则几个文件同时重试会立刻把配额重新顶满；网络抖动一类的错误短暂退避即可。
func (f *FS) copyRetryWait(attempt int, err error) time.Duration {
	if f.retryBackoff != nil {
		return f.retryBackoff(attempt, err)
	}
	if util.IsThrottled(err) {
		return util.ThrottleWait(attempt, "")
	}
	return time.Duration(attempt) * time.Second
}

// copyOne 复制单个文件，文件级重试 2 次（共 3 次尝试）；失败重试前回退已计进度。
// 重试之间会退避——撞限流时立刻重连只会把配额烧得更快，本来能自愈的也救不回来。
func (f *FS) copyOne(ctx context.Context, sm *Mount, up driver.Uploader, fj fileJob, pr Progress) error {
	pr.FileStart(fj.name)
	defer pr.FileDone(fj.name) // 含重试耗尽、ctx 取消等所有出口，不留悬空在途项
	var lastErr error
	stage := stageSource
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cr := &countingReader{ctx: ctx, pr: pr}
		stage = stageSource
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
			stage = stageDest
			return up.Put(ctx, fj.dstDirRel, fj.name, cr, fj.size)
		}()
		if err == nil {
			return nil
		}
		// 上传中途失败时，如果源流上报过读取错误，真正的锅在源端而不是目标端
		if stage == stageDest && cr.readErr != nil {
			stage = stageSource
		}
		lastErr = err
		pr.Add(-cr.n) // 回退本次已计字节
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == 3 {
			break
		}
		if wait := f.copyRetryWait(attempt, err); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	// 退避重试全用完还在被限流，就不是瞬时抖动了——要么并发开太大，要么云盘的每日
	// 上传额度已经用尽（如 Google Drive 每天 750GB）。两者返回同一个错误码，分不出来，
	// 但都不该再劝用户「稍后重试」：得给出能动手的下一步。
	if util.IsThrottled(lastErr) {
		return util.Messagef(lastErr,
			"复制 %s 失败（%s）：反复被限流。请调低「文件夹内并发」后重试；若持续如此，"+
				"可能是云盘每日上传额度已用尽，明天再继续。已传完的文件不会重传。",
			fj.srcRel, stage)
	}
	// 带上完整路径与阶段：Humanize 会保留这句上下文，只把内层技术错误翻成人话，
	// 用户因此能看出是哪个文件、哪一侧出的问题，而不是光一句「请求过于频繁」。
	return util.Contextf(lastErr, "复制 %s 失败（%s，已重试 2 次）", fj.srcRel, stage)
}

// countingReader 包装源读取流，把读到的字节数上报进度；
// 每次 Read 前检查 ctx——本地文件句柄不走 HTTP，不查 ctx 的话取消无法中断 io.Copy。
type countingReader struct {
	ctx     context.Context
	r       io.Reader
	pr      Progress
	n       int64
	readErr error // 源端读取错误：上传中途挂了时用来判断锅在哪一侧
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
	if err != nil && !errors.Is(err, io.EOF) {
		c.readErr = err
	}
	return n, err
}
