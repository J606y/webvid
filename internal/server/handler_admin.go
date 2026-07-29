package server

import (
	"errors"

	"github.com/gin-gonic/gin"

	"newlist/internal/index"
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
		"preload_workers":   s.conf.PreloadWorkers(),
		"media_jobs":        s.conf.MediaJobs(),
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
		PreloadWorkers  *int   `json:"preload_workers"`
		MediaJobs       *int   `json:"media_jobs"`
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
		{req.PreloadWorkers, "preload_workers", 1, 16},
		{req.MediaJobs, "media_jobs", 1, 8},
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
	// ffmpeg/ffprobe 总闸：抽封面与探源信息各一处，下一件活起跑即按新值排队
	s.thumbs.SetJobs(s.conf.MediaJobs())
	s.media.SetJobs(s.conf.MediaJobs())
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

// POST /api/admin/index/clear —— 删除索引（清空 files 表，文件本身不动）。
func (s *Server) indexClear(c *gin.Context) {
	if err := s.index.Clear(); err != nil {
		if errors.Is(err, index.ErrBusy) {
			Fail(c, 409, "索引重建进行中，请稍后再试")
			return
		}
		Fail500(c, err)
		return
	}
	OK(c, s.index.Progress())
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

// POST /api/admin/preload/clear —— 删除已缓存的封面与视频源信息（先停下在跑的预载）。
func (s *Server) preloadClear(c *gin.Context) {
	if s.preload == nil {
		Fail(c, 501, "预载不可用")
		return
	}
	res, err := s.preload.Clear()
	if err != nil {
		Fail500(c, err)
		return
	}
	OK(c, res)
}

// POST /api/admin/preload/snooze —— 「不是现在」：停下预载，一天后自动继续。
func (s *Server) preloadSnooze(c *gin.Context) {
	if s.preload == nil {
		Fail(c, 501, "预载不可用")
		return
	}
	s.preload.Snooze()
	OK(c, s.preload.Progress())
}

// POST /api/admin/preload/resume —— 「继续」：提前结束推迟，接着剩下的预载。
func (s *Server) preloadResume(c *gin.Context) {
	if s.preload == nil {
		Fail(c, 501, "预载不可用")
		return
	}
	s.preload.Resume()
	OK(c, s.preload.Progress())
}
