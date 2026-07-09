package server

import (
	"errors"

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
