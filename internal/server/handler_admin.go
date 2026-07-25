package server

import (
	"github.com/gin-gonic/gin"

	"newlist/internal/task"
)

// settingsMap 当前生效的设置。一律读回 conf，因此拿到的恒是钳位后的真实值。
func (s *Server) settingsMap() gin.H {
	return gin.H{
		"site_title":        s.conf.SiteTitle(),
		"copy_workers":      s.conf.CopyWorkers(),
		"copy_file_workers": s.conf.CopyFileWorkers(),
		"offline_workers":   s.conf.OfflineWorkers(),
		"upload_workers":    s.conf.UploadWorkers(),
		"copy_speed_kb":     s.conf.CopySpeedKB(),
		"upload_speed_kb":   s.conf.UploadSpeedKB(),
		"download_speed_kb": s.conf.DownloadSpeedKB(),
		"media_home_sort":   s.conf.MediaHomeSort(),
	}
}

// GET /api/admin/settings
func (s *Server) settingsGet(c *gin.Context) {
	OK(c, s.settingsMap())
}

// PUT /api/admin/settings {site_title, *_workers, *_speed_kb}
// 线程数与限速保存后立即生效（含在途流），无需重启。缺省字段（指针 nil）保持原值。
func (s *Server) settingsPut(c *gin.Context) {
	var req struct {
		SiteTitle       string `json:"site_title"`
		CopyWorkers     *int   `json:"copy_workers"`
		CopyFileWorkers *int   `json:"copy_file_workers"`
		OfflineWorkers  *int   `json:"offline_workers"`
		UploadWorkers   *int   `json:"upload_workers"`
		CopySpeedKB     *int   `json:"copy_speed_kb"`
		UploadSpeedKB   *int   `json:"upload_speed_kb"`
		DownloadSpeedKB *int   `json:"download_speed_kb"`
		MediaHomeSort   string `json:"media_home_sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SiteTitle == "" {
		Fail(c, 400, "站点标题不能为空")
		return
	}
	if err := s.conf.Set("site_title", req.SiteTitle); err != nil {
		Fail500(c, err)
		return
	}
	// 媒体库首页取法：只认这两个值，其余（含缺省）保持原状不动
	if req.MediaHomeSort == "random" || req.MediaHomeSort == "modified" {
		if err := s.conf.Set("media_home_sort", req.MediaHomeSort); err != nil {
			Fail500(c, err)
			return
		}
	}
	for _, f := range []struct {
		v      *int
		key    string
		lo, hi int
	}{
		{req.CopyWorkers, "copy_workers", 1, 32},
		{req.CopyFileWorkers, "copy_file_workers", 1, 32},
		{req.OfflineWorkers, "offline_workers", 1, 32},
		{req.UploadWorkers, "upload_workers", 1, 8},
		{req.CopySpeedKB, "copy_speed_kb", 0, 1 << 20},
		{req.UploadSpeedKB, "upload_speed_kb", 0, 1 << 20},
		{req.DownloadSpeedKB, "download_speed_kb", 0, 1 << 20},
	} {
		if f.v == nil {
			continue
		}
		if _, err := s.conf.SetInt(f.key, *f.v, f.lo, f.hi); err != nil {
			Fail500(c, err)
			return
		}
	}
	// 热生效：worker 池扩缩、限速即时应用（读回 conf 拿钳位后的值）
	s.tasks.SetWorkers(task.GroupCopy, s.conf.CopyWorkers())
	s.fs.SetCopyFileWorkers(s.conf.CopyFileWorkers())
	s.tasks.SetWorkers(task.GroupOffline, s.conf.OfflineWorkers())
	s.limCopy.SetKBps(s.conf.CopySpeedKB())
	s.limUp.SetKBps(s.conf.UploadSpeedKB())
	s.limDown.SetKBps(s.conf.DownloadSpeedKB())
	// 回传钳位后的设置：超出范围的输入会被静默夹到边界，前端据此回填，
	// 避免表单停留在填进去的数字上、与真正生效的值对不上。
	OK(c, s.settingsMap())
}

// GET /api/admin/index/progress
func (s *Server) indexProgress(c *gin.Context) {
	OK(c, s.index.Progress())
}

// POST /api/admin/index/rebuild
func (s *Server) indexRebuild(c *gin.Context) {
	if !s.index.Rebuild() {
		Fail(c, 409, "索引重建已在进行中")
		return
	}
	OK(c, nil)
}

// GET /api/admin/preload/progress —— 后台封面/源信息预载进度。
func (s *Server) preloadProgress(c *gin.Context) {
	if s.preload == nil {
		OK(c, gin.H{"running": false})
		return
	}
	OK(c, s.preload.Progress())
}

// POST /api/admin/preload/run —— 手动触发一轮预载（取代进行中的旧轮）。
func (s *Server) preloadRun(c *gin.Context) {
	if s.preload == nil {
		Fail(c, 501, "预载不可用")
		return
	}
	s.preload.Run()
	OK(c, nil)
}
