// Package local 实现本地磁盘驱动。
// 所有文件操作经 *os.Root 完成：内核层面拒绝任何越出根目录的访问（含符号链接逃逸），
// 是防目录穿越的最后一道防线（第一道在 fs 层的路径归一化）。
package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"newlist/internal/driver"
	"newlist/internal/model"
)

func init() {
	driver.Register(driver.Meta{
		Name:   "local",
		Label:  "本地磁盘",
		Remote: false,
		Fields: []driver.FieldSpec{
			{Name: "root_path", Label: "根目录路径", Type: "string", Required: true,
				Help: "宿主机目录的绝对路径；Docker 内通常为挂载进来的 /files"},
		},
	}, func() driver.Driver { return &Local{} })
}

type Local struct {
	root    *os.Root
	rootAbs string
}

func (l *Local) Init(_ context.Context, cfg driver.Config) error {
	rp := strings.TrimSpace(cfg["root_path"])
	if rp == "" {
		return errors.New("root_path 不能为空")
	}
	abs, err := filepath.Abs(rp)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("创建根目录失败: %w", err)
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return fmt.Errorf("打开根目录失败: %w", err)
	}
	l.root, l.rootAbs = r, abs
	return nil
}

func (l *Local) Drop() error {
	if l.root != nil {
		return l.root.Close()
	}
	return nil
}

// dot 把空的相对路径转成 "."（os.Root 的根表示）。
func dot(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, iofs.ErrNotExist):
		return driver.ErrNotFound
	case errors.Is(err, iofs.ErrExist):
		return driver.ErrExist
	case isPathEscape(err):
		// os.Root 拒绝越界，一律按不存在处理，不暴露细节。
		return driver.ErrNotFound
	default:
		return err
	}
}

// isPathEscape 判断 err 是否为 os.Root 的越界拒绝。Go 标准库未导出该 sentinel，
// 只能匹配文案 "path escapes from parent"；集中此一处，配 local_test.go 的
// TestPathEscapeSentinel 监视标准库文案变更（变更时该测试先红给出信号）。
func isPathEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "escapes from parent")
}

// checkName 校验单个文件/目录名在 Windows/NTFS 上的合法性。
func checkName(name string) error {
	if name == "" || name == "." || name == ".." {
		return driver.ErrBadName
	}
	if strings.ContainsAny(name, `<>:"/\|?*`) {
		return driver.ErrBadName
	}
	for _, r := range name {
		if r < 0x20 {
			return driver.ErrBadName
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return driver.ErrBadName
	}
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return driver.ErrBadName
	}
	return nil
}

func checkSegments(rel string) error {
	if rel == "" {
		return nil
	}
	for _, seg := range strings.Split(rel, "/") {
		if err := checkName(seg); err != nil {
			return err
		}
	}
	return nil
}

func fileInfo(name string, fi iofs.FileInfo) model.FileInfo {
	size := fi.Size()
	if fi.IsDir() {
		size = 0
	}
	return model.FileInfo{Name: name, Size: size, IsDir: fi.IsDir(), Modified: fi.ModTime().UTC()}
}

func (l *Local) List(_ context.Context, rel string) ([]model.FileInfo, error) {
	f, err := l.root.Open(dot(rel))
	if err != nil {
		return nil, mapErr(err)
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]model.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue // 条目在遍历期间被删，跳过
		}
		out = append(out, fileInfo(e.Name(), info))
	}
	return out, nil
}

func (l *Local) Stat(_ context.Context, rel string) (model.FileInfo, error) {
	fi, err := l.root.Stat(dot(rel))
	if err != nil {
		return model.FileInfo{}, mapErr(err)
	}
	name := path.Base(rel)
	if rel == "" {
		name = "/"
	}
	return fileInfo(name, fi), nil
}

func (l *Local) Link(_ context.Context, rel string) (*driver.Link, error) {
	f, err := l.root.Open(dot(rel))
	if err != nil {
		return nil, mapErr(err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, mapErr(err)
	}
	if fi.IsDir() {
		f.Close()
		return nil, driver.ErrNotFound
	}
	return &driver.Link{Local: f, Size: fi.Size(), Mod: fi.ModTime().UTC()}, nil
}

func (l *Local) MakeDir(_ context.Context, rel string) error {
	if err := checkSegments(rel); err != nil {
		return err
	}
	if _, err := l.root.Stat(dot(rel)); err == nil {
		return driver.ErrExist
	}
	return mapErr(l.root.MkdirAll(rel, 0o755))
}

func (l *Local) Rename(_ context.Context, rel, newName string) error {
	if err := checkName(newName); err != nil {
		return err
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	newRel := path.Join(dir, newName)
	if newRel == rel {
		return nil
	}
	if _, err := l.root.Stat(newRel); err == nil {
		return driver.ErrExist
	}
	return mapErr(l.root.Rename(rel, newRel))
}

func (l *Local) Remove(_ context.Context, rel string) error {
	if rel == "" {
		return errors.New("拒绝删除存储根目录")
	}
	if _, err := l.root.Stat(rel); err != nil {
		return mapErr(err)
	}
	return mapErr(l.root.RemoveAll(rel))
}

func (l *Local) Move(_ context.Context, srcRel, dstDirRel string) error {
	if srcRel == "" {
		return driver.ErrNotSupported
	}
	dst := path.Join(dstDirRel, path.Base(srcRel))
	if dst == srcRel {
		return nil
	}
	if dst == srcRel || strings.HasPrefix(dst+"/", srcRel+"/") {
		return errors.New("不能移动到自身内部")
	}
	if _, err := l.root.Stat(dst); err == nil {
		return driver.ErrExist
	}
	return mapErr(l.root.Rename(srcRel, dst))
}

func (l *Local) Copy(ctx context.Context, srcRel, dstDirRel string) error {
	if srcRel == "" {
		return driver.ErrNotSupported
	}
	dst := path.Join(dstDirRel, path.Base(srcRel))
	if dst == srcRel || strings.HasPrefix(dst+"/", srcRel+"/") {
		return errors.New("不能复制到自身内部")
	}
	return l.copyItem(ctx, srcRel, dst)
}

func (l *Local) copyItem(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := l.root.Stat(src)
	if err != nil {
		return mapErr(err)
	}
	if fi.IsDir() {
		if err := l.root.MkdirAll(dst, 0o755); err != nil && !errors.Is(err, iofs.ErrExist) {
			return mapErr(err)
		}
		items, err := l.List(ctx, src)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := l.copyItem(ctx, src+"/"+it.Name, dst+"/"+it.Name); err != nil {
				return err
			}
		}
		return nil
	}
	sf, err := l.root.Open(src)
	if err != nil {
		return mapErr(err)
	}
	defer sf.Close()
	df, err := l.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return mapErr(err)
	}
	if _, err := io.Copy(df, sf); err != nil {
		df.Close()
		l.root.Remove(dst)
		return err
	}
	return df.Close()
}

// Put 流式写入：先写同目录隐藏临时文件，成功后原子改名，杜绝半截文件。
func (l *Local) Put(_ context.Context, dstDirRel, name string, r io.Reader, _ int64) error {
	if err := checkName(name); err != nil {
		return err
	}
	if dstDirRel != "" {
		if err := l.root.MkdirAll(dstDirRel, 0o755); err != nil && !errors.Is(err, iofs.ErrExist) {
			return mapErr(err)
		}
	}
	var rnd [4]byte
	rand.Read(rnd[:])
	tmp := path.Join(dstDirRel, "."+name+".uploading-"+hex.EncodeToString(rnd[:]))
	f, err := l.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return mapErr(err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		l.root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		l.root.Remove(tmp)
		return err
	}
	dst := path.Join(dstDirRel, name)
	if err := l.root.Rename(tmp, dst); err != nil {
		l.root.Remove(tmp)
		return mapErr(err)
	}
	return nil
}

// AbsPath 返回条目的宿主机绝对路径（供 ffmpeg 缩略图/转码使用）。
func (l *Local) AbsPath(rel string) (string, error) {
	abs := filepath.Clean(filepath.Join(l.rootAbs, filepath.FromSlash(rel)))
	if abs != l.rootAbs && !strings.HasPrefix(abs, l.rootAbs+string(filepath.Separator)) {
		return "", driver.ErrNotFound
	}
	return abs, nil
}
