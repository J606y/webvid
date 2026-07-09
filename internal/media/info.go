package media

import (
	"context"
	"errors"

	"newlist/internal/model"
	"newlist/internal/user"
)

// directPlayExts 浏览器原生可播容器：不起 ffmpeg、进页秒开（与前端 Play.vue
// DIRECT_EXTS 同表）。其余视频格式经 ffprobe 决策走 HLS（remux/转码）。
var directPlayExts = map[string]bool{
	"mp4": true, "m4v": true, "mov": true, "webm": true,
}

// IsDirectExt 报告扩展名是否为浏览器原生可播容器（无需探测）。
func IsDirectExt(name string) bool { return directPlayExts[model.Ext(name)] }

// PlayInfo 是 /video/info 的完整播放策略结论（direct/hls/unsupported 三档）。
type PlayInfo struct {
	Strategy string  `json:"strategy"`           // direct | hls | unsupported
	Reason   string  `json:"reason,omitempty"`   // remux | transcode（仅 hls）
	Duration float64 `json:"duration,omitempty"` // 秒（仅 hls）
	Message  string  `json:"message,omitempty"`  // unsupported 降级文案
}

// Info 解析文件的播放策略：direct 扩展名秒回、非视频 unsupported、其余经 Decide
// 探测（持久缓存命中则免现场探测）。探测失败降级为 unsupported 并给出文案。
func (s *Service) Info(ctx context.Context, u *user.User, logical string, fi model.FileInfo) PlayInfo {
	if directPlayExts[model.Ext(fi.Name)] {
		return PlayInfo{Strategy: "direct"}
	}
	if model.ExtType(fi.Name) != "video" {
		return PlayInfo{Strategy: "unsupported", Message: "该文件不是可播放的视频格式"}
	}
	dec, err := s.Decide(ctx, u, logical, fi)
	if err != nil {
		msg := "该视频暂时无法转码播放，可下载后本地观看"
		if errors.Is(err, ErrNoFFmpeg) {
			msg = "服务器未安装 ffmpeg，无法转码播放该格式"
		}
		return PlayInfo{Strategy: "unsupported", Message: msg}
	}
	reason := "transcode"
	if dec.VideoCopy {
		reason = "remux"
	}
	return PlayInfo{Strategy: "hls", Reason: reason, Duration: dec.Duration}
}
