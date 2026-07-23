package driver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"newlist/internal/model"
)

// 哨兵错误：驱动实现统一返回这些，handler 层映射 HTTP 状态码。
var (
	ErrNotFound     = errors.New("对象不存在")
	ErrExist        = errors.New("目标已存在")
	ErrNotSupported = errors.New("该存储不支持此操作")
	ErrBadName      = errors.New("名称包含非法字符或为保留名")
	ErrDenied       = errors.New("存储拒绝了该操作（权限不足）") // 云盘写被拒：如 OneDrive accessDenied/403
	ErrQuota        = errors.New("存储空间已满")               // 云盘配额用尽：如 OneDrive quotaLimitReached/507
	ErrUpstream     = errors.New("存储返回错误")               // 其余云盘 API 错误：包裹原始信息透传给用户，避免不透明 500
)

// Config 是存储配置（storages.config 的 JSON 反序列化结果）。
type Config map[string]string

// Link 是获取文件内容的方式，二选一：
// 云盘驱动填 URL(+Header)，由上层 302 或代理；本地驱动填 Local 句柄。
type Link struct {
	URL    string
	Header http.Header
	Local  io.ReadSeekCloser
	Size   int64
	Mod    time.Time
}

// Driver 是所有存储驱动的必备能力（只读）。
// relPath 为相对存储根的路径，"" 表示根；分隔符恒为 "/"。
type Driver interface {
	Init(ctx context.Context, cfg Config) error
	Drop() error
	List(ctx context.Context, relPath string) ([]model.FileInfo, error)
	Stat(ctx context.Context, relPath string) (model.FileInfo, error)
	Link(ctx context.Context, relPath string) (*Link, error)
}

// Writer 可选：目录/条目管理能力。
type Writer interface {
	MakeDir(ctx context.Context, relPath string) error
	Rename(ctx context.Context, relPath, newName string) error
	Remove(ctx context.Context, relPath string) error
	Move(ctx context.Context, srcRel, dstDirRel string) error
	Copy(ctx context.Context, srcRel, dstDirRel string) error
}

// Uploader 可选：上传能力。size 已知时 >=0，未知为 -1。
type Uploader interface {
	Put(ctx context.Context, dstDirRel, name string, r io.Reader, size int64) error
}

// Thumber 可选：存储自带缩略图（返回可 302 的 URL）。
type Thumber interface {
	Thumb(ctx context.Context, relPath string) (string, error)
}

// LocalPather 可选：能给出条目的宿主机绝对路径（供 ffmpeg 等外部进程使用）。
type LocalPather interface {
	AbsPath(relPath string) (string, error)
}

// LinkRefresher 可选：直链确认已失效（拉流 401/403）时强制重取，绕过驱动内缓存。
// 未实现则退化为再调 Link——但带缓存的驱动会在 TTL 内返回同一条死链，
// 换链形同虚设，重连持续失败到缓存过期为止。
type LinkRefresher interface {
	RefreshLink(ctx context.Context, relPath string) (*Link, error)
}

// ConfigPersister 可选：驱动配置在运行期会变化（如 OAuth refresh_token 轮换），
// fs 层在 Init 前注入保存回调，驱动在配置变化后调用以持久化。
type ConfigPersister interface {
	SetPersist(func(cfg Config) error)
}
