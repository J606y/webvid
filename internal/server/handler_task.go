package server

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/task"
)

func taskError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, task.ErrNotFound):
		Fail(c, 404, "任务不存在")
	case errors.Is(err, task.ErrForbidden):
		Fail(c, 403, "无权操作该任务")
	case errors.Is(err, task.ErrBadState):
		Fail(c, 409, "任务当前状态不允许此操作")
	default:
		Fail500(c, err)
	}
}

// GET /api/tasks
func (s *Server) taskList(c *gin.Context) {
	u := getUser(c)
	OK(c, s.tasks.List(u.ID, u.IsAdmin()))
}

// fileStates 是清单允许的筛选值；传别的（含手滑的错别字）一律当「全部」，
// 免得筛出个空列表让人以为文件没了。
var fileStates = map[task.FileState]bool{
	task.FilePending: true, task.FileRunning: true, task.FileDone: true,
	task.FileSkipped: true, task.FileError: true,
}

// GET /api/tasks/:id/files —— 任务的文件清单：按状态筛、按路径搜、分页取。
// 一个文件夹转存动辄几万条，只按需给一页。
func (s *Server) taskFiles(c *gin.Context) {
	u := getUser(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "0"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	q := task.FilesQuery{
		Q:      strings.TrimSpace(c.Query("q")),
		Offset: offset,
		Limit:  limit,
	}
	if st := task.FileState(c.Query("state")); fileStates[st] {
		q.State = st
	}
	page, err := s.tasks.Files(c.Param("id"), u.ID, u.IsAdmin(), q)
	if err != nil {
		taskError(c, err)
		return
	}
	OK(c, page)
}

// POST /api/tasks/:id/cancel
func (s *Server) taskCancel(c *gin.Context) {
	u := getUser(c)
	if err := s.tasks.Cancel(c.Param("id"), u.ID, u.IsAdmin()); err != nil {
		taskError(c, err)
		return
	}
	OK(c, nil)
}

// POST /api/tasks/:id/retry
func (s *Server) taskRetry(c *gin.Context) {
	u := getUser(c)
	if err := s.tasks.Retry(c.Param("id"), u.ID, u.IsAdmin()); err != nil {
		taskError(c, err)
		return
	}
	OK(c, nil)
}

// DELETE /api/tasks/done —— 只清除已成功任务，失败/取消保留以便重试
func (s *Server) taskClearDone(c *gin.Context) {
	u := getUser(c)
	s.tasks.ClearDone(u.ID, u.IsAdmin())
	OK(c, nil)
}

// POST /api/tasks/:id/remove —— 删除单个终态任务
func (s *Server) taskRemove(c *gin.Context) {
	u := getUser(c)
	if err := s.tasks.Remove(c.Param("id"), u.ID, u.IsAdmin()); err != nil {
		taskError(c, err)
		return
	}
	OK(c, nil)
}
