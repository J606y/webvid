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

// ProbeState 是某个文件的源信息此刻在缓存里的处境。
type ProbeState int

const (
	ProbePending ProbeState = iota // 还没探测过，要真去跑一趟 ffprobe
	ProbeReady                     // 缓存已有且与当前 size/mtime 一致，无需再探
	ProbeNone                      // 压根不需要或探不了（direct 扩展名秒判、无 ffprobe）
)

// ProbeStatus 只查缓存判断某文件还要不要探测，绝不触发 ffprobe，也不发网络请求。
// 供后台预载在派活前把「没活可干」的项排除在进度总数之外——它们会被瞬间跳过，
// 算进总数只会让进度条一开局就停在已缓存占比上。
// 命中判据与 Decide 走的 loadInfo 是同一套（path + size + modified 全等才算数）。
func (s *Service) ProbeStatus(logical string, fi model.FileInfo) ProbeState {
	if model.ExtType(fi.Name) != "video" || IsDirectExt(fi.Name) {
		return ProbeNone
	}
	if _, ok := s.loadInfo(logical, fi); ok {
		return ProbeReady
	}
	if _, ffprobe := s.tools(); ffprobe == "" {
		return ProbeNone
	}
	return ProbePending
}

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
