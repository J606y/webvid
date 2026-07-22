package server

import (
	"context"
	"path"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/fs"
	"newlist/internal/model"
	"newlist/internal/task"
	"newlist/internal/util"
)

// GET /api/fs/get?path=
func (s *Server) fsGet(c *gin.Context) {
	fi, err := s.fs.Get(c.Request.Context(), getUser(c), c.Query("path"))
	if err != nil {
		fsError(c, err)
		return
	}
	OK(c, fi)
}

// GET /api/fs/list?path= → {items, write, upload}
func (s *Server) fsList(c *gin.Context) {
	u := getUser(c)
	p := c.Query("path")
	items, err := s.fs.List(c.Request.Context(), u, p)
	if err != nil {
		fsError(c, err)
		return
	}
	caps := s.fs.Caps(u, p)
	OK(c, gin.H{"items": items, "write": caps.Write, "upload": caps.Upload})
}

// POST /api/fs/mkdir {path}
func (s *Server) fsMkdir(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
		Fail(c, 400, "path 不能为空")
		return
	}
	p, err := fs.NormPath(req.Path)
	if err != nil {
		fsError(c, err)
		return
	}
	if err := s.fs.MakeDir(c.Request.Context(), getUser(c), p); err != nil {
		fsError(c, err)
		return
	}
	s.index.Upsert(p, model.FileInfo{Name: path.Base(p), IsDir: true, Modified: time.Now().UTC()})
	OK(c, nil)
}

// POST /api/fs/rename {path, name}
func (s *Server) fsRename(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" || req.Name == "" {
		Fail(c, 400, "path/name 不能为空")
		return
	}
	p, err := fs.NormPath(req.Path)
	if err != nil {
		fsError(c, err)
		return
	}
	if err := s.fs.Rename(c.Request.Context(), getUser(c), p, req.Name); err != nil {
		fsError(c, err)
		return
	}
	s.index.RenamePrefix(p, util.JoinLogical(path.Dir(p), req.Name))
	OK(c, nil)
}

// POST /api/fs/remove {paths[]}
func (s *Server) fsRemove(c *gin.Context) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Paths) == 0 {
		Fail(c, 400, "paths 不能为空")
		return
	}
	for _, raw := range req.Paths {
		p, err := fs.NormPath(raw)
		if err != nil {
			fsError(c, err)
			return
		}
		if err := s.fs.Remove(c.Request.Context(), getUser(c), p); err != nil {
			fsError(c, err)
			return
		}
		s.index.DeletePrefix(p)
	}
	OK(c, nil)
}

// POST /api/fs/move | /api/fs/copy {paths[], dst_dir}
// 同存储：驱动原生同步 move/copy；跨存储：建后台转存任务，返回 task_ids。
func (s *Server) fsMoveCopy(isMove bool) gin.HandlerFunc {
	verb := "复制"
	if isMove {
		verb = "移动"
	}
	return func(c *gin.Context) {
		var req struct {
			Paths  []string `json:"paths"`
			DstDir string   `json:"dst_dir"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Paths) == 0 || req.DstDir == "" {
			Fail(c, 400, "paths/dst_dir 不能为空")
			return
		}
		dst, err := fs.NormPath(req.DstDir)
		if err != nil {
			fsError(c, err)
			return
		}
		u := getUser(c)
		var taskIDs []string
		var errs []string
		for _, raw := range req.Paths {
			p, err := fs.NormPath(raw)
			if err != nil {
				fsError(c, err)
				return
			}
			same, upOK, err := s.fs.SameStorage(u, p, dst)
			if err != nil {
				fsError(c, err)
				return
			}
			target := util.JoinLogical(dst, path.Base(p))
			if same {
				if err := s.fs.MoveCopy(c.Request.Context(), u, p, dst, isMove); err != nil {
					fsError(c, err)
					return
				}
				if isMove {
					s.index.RenamePrefix(p, target)
				} else {
					s.index.ScanSubtree(target)
				}
				continue
			}
			if !upOK {
				errs = append(errs, path.Base(p)+": 目标存储不支持写入（该存储只能作为转存源）")
				continue
			}
			src, dstDir, tgt := p, dst, target // 闭包取副本
			t := s.tasks.Submit(u.ID, verb+" "+path.Base(p)+" → "+dst,
				func(ctx context.Context, t *task.Task) error {
					if err := s.fs.Transfer(ctx, u, src, dstDir, isMove, t); err != nil {
						return err
					}
					if isMove {
						s.index.RenamePrefix(src, tgt)
					} else {
						s.index.ScanSubtree(tgt)
					}
					return nil
				})
			taskIDs = append(taskIDs, t.ID)
		}
		OK(c, gin.H{"task_ids": taskIDs, "errors": errs})
	}
}

// PUT /api/fs/upload?path=<目标文件全路径>&overwrite=1 —— body 为原始字节流。
func (s *Server) fsUpload(c *gin.Context) {
	p, err := fs.NormPath(c.Query("path"))
	if err != nil {
		fsError(c, err)
		return
	}
	if p == "/" {
		Fail(c, 400, "path 必须是文件路径")
		return
	}
	dir, name := path.Dir(p), path.Base(p)
	overwrite := c.Query("overwrite") == "1" || c.Query("overwrite") == "true"
	u := getUser(c)
	// 上传限速：所有上传连接共享全站速率
	body := s.limUp.Reader(c.Request.Context(), c.Request.Body)
	err = s.fs.Put(c.Request.Context(), u, dir, name, body, c.Request.ContentLength, overwrite)
	if err != nil {
		fsError(c, err)
		return
	}
	// 上传成功后回查真实大小/时间写索引
	if fi, err := s.fs.Get(c.Request.Context(), u, p); err == nil {
		s.index.Upsert(p, fi)
	}
	OK(c, nil)
}
