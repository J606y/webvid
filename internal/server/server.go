// Package server 提供 HTTP API 与静态资源服务。
package server

import (
	"database/sql"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/limiter"
	"newlist/internal/media"
	"newlist/internal/preload"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

type Server struct {
	db      *sql.DB
	conf    *conf.Store
	users   *user.Store
	fs      *fs.FS
	thumbs  *thumb.Service
	media   *media.Service
	index   *index.Builder
	preload *preload.Service
	tasks   *task.Manager
	secret  []byte
	limiter auth.LoginLimiter

	// 全站限速器：三条链路各一，速率来自 settings，可在后台热调整。
	limUp   *limiter.Limiter // 网页上传（PUT /fs/upload 请求体）
	limDown *limiter.Limiter // 服务器下行（/raw 本地与代理中转）+ 离线下载拉流
	limCopy *limiter.Limiter // 跨存储复制/移动任务（注入 fs.Transfer）
}

func New(db *sql.DB, cf *conf.Store, us *user.Store, f *fs.FS,
	th *thumb.Service, md *media.Service, idx *index.Builder, pl *preload.Service,
	tasks *task.Manager, secret []byte) *Server {
	s := &Server{db: db, conf: cf, users: us, fs: f, thumbs: th, media: md,
		index: idx, preload: pl, tasks: tasks, secret: secret,
		limUp: limiter.New(), limDown: limiter.New(), limCopy: limiter.New()}
	s.limUp.SetKBps(cf.UploadSpeedKB())
	s.limDown.SetKBps(cf.DownloadSpeedKB())
	s.limCopy.SetKBps(cf.CopySpeedKB())
	f.SetCopyLimiter(s.limCopy)
	tasks.SetWorkers(task.GroupOffline, cf.OfflineWorkers())
	return s
}
