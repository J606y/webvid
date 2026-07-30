package media

import (
	"context"
	"errors"
	"log"

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

// ProbedSet 是「已探测过哪些文件」的一次性快照，由 Probed 拉出、交给 ProbeStatus 比对。
type ProbedSet map[string]probedRow

type probedRow struct {
	size int64
	mod  string
}

// Probed 一次拉出全部探测缓存。
//
// 后台预载清点时要对全库每个视频判一次「还要不要探测」，一项一条 SELECT 就是 N+1：
// 几万条媒体要好几秒，还全挤在 SQLite 那四条连接上，把同时在跑的索引进度查询一起拖住。
// 一次拉进内存再比对，几万行也就一次查询。db 为 nil（测试）时返回空集，等同全未探测。
func (s *Service) Probed() ProbedSet {
	set := ProbedSet{}
	if s.db == nil {
		return set
	}
	rows, err := s.db.Query(`SELECT path, size, modified FROM media_info`)
	if err != nil {
		log.Printf("[media] 读取探测缓存失败: %v", err)
		return set
	}
	defer rows.Close()
	for rows.Next() {
		var (
			path string
			r    probedRow
		)
		if err := rows.Scan(&path, &r.size, &r.mod); err != nil {
			continue
		}
		set[path] = r
	}
	return set
}

// ProbeStatus 只查缓存判断某文件还要不要探测，绝不触发 ffprobe，也不发网络请求。
// 供后台预载在派活前把「没活可干」的项排除在进度总数之外——它们会被瞬间跳过，
// 算进总数只会让进度条一开局就停在已缓存占比上。
// cached 由 Probed 预先拉好；命中判据与 Decide 走的 loadInfo 是同一套
// （path + size + modified 全等才算数）。
func (s *Service) ProbeStatus(cached ProbedSet, logical string, fi model.FileInfo) ProbeState {
	if model.ExtType(fi.Name) != "video" || IsDirectExt(fi.Name) {
		return ProbeNone
	}
	if r, ok := cached[logical]; ok && r.size == fi.Size && r.mod == modKey(fi.Modified) {
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
	// HEVC 为真表示这次之所以能直出，靠的是客户端自报的 HEVC 解码能力。
	// 前端据此知道「万一真解不动，该降级重试的是这一项」，不必对着所有解码失败瞎猜。
	HEVC bool `json:"hevc,omitempty"`
}

// Info 解析文件的播放策略：direct 扩展名秒回、非视频 unsupported、其余经探测
// （持久缓存命中则免现场探测）。探测失败降级为 unsupported 并给出文案。
//
// hevcCap 是客户端自报的 HEVC 解码能力（0 / 8 / 10，见 Decision.CopyWith）：支持的
// 设备上 HEVC 直接原样封装，一个核都不用；不支持的照旧重编码。
func (s *Service) Info(ctx context.Context, u *user.User, logical string, fi model.FileInfo, hevcCap int) PlayInfo {
	if directPlayExts[model.Ext(fi.Name)] {
		return PlayInfo{Strategy: "direct"}
	}
	if model.ExtType(fi.Name) != "video" {
		return PlayInfo{Strategy: "unsupported", Message: "该文件不是可播放的视频格式"}
	}
	// 紧接着的 index.m3u8 会用同一份 size/mtime 建会话，那时不必再问一次云盘
	s.rememberStat(logical, fi)
	raw, err := s.decideNow(ctx, u, logical, fi, true) // 用户正等着这次探测的结果
	if err != nil {
		msg := "该视频暂时无法转码播放，可下载后本地观看"
		if errors.Is(err, ErrNoFFmpeg) {
			msg = "服务器未安装 ffmpeg，无法转码播放该格式"
		}
		return PlayInfo{Strategy: "unsupported", Message: msg}
	}
	dec := raw.CopyWith(hevcCap)
	reason := "transcode"
	if dec.VideoCopy {
		reason = "remux"
	}
	return PlayInfo{Strategy: "hls", Reason: reason, Duration: dec.Duration,
		HEVC: dec.videoTag == "hvc1"}
}
