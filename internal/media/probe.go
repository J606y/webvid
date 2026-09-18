// Package media 提供视频探测与 HLS 转码播放（M10）。
// 三档策略：direct 由 handler 按扩展名先行判定（mp4 系/webm 不起 ffmpeg 秒开）；
// 其余格式经 ffprobe 决策 —— 视频可播只换封装（remux，-c copy 零 CPU）、
// 视频可播音频不可播只转音频、否则 libx264 重编码。会话管理见 hls.go。
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNoFFmpeg 服务器缺少 ffmpeg/ffprobe（handler 映射为 unsupported 降级）。
var ErrNoFFmpeg = errors.New("服务器未安装 ffmpeg，无法转码播放")

// LookTool 探测 ffmpeg/ffprobe 可执行文件路径（"" = 不可用）：
// 环境变量 NL_FFMPEG / NL_FFPROBE 优先，其次 PATH，最后 winget 安装目录兜底。
func LookTool(name string) string {
	if p := os.Getenv("NL_" + strings.ToUpper(name)); p != "" {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		matches, _ := filepath.Glob(filepath.Join(la,
			"Microsoft", "WinGet", "Packages", "Gyan.FFmpeg*", "ffmpeg-*", "bin", name+".exe"))
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
	}
	return ""
}

type probeStream struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	PixFmt    string `json:"pix_fmt"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	// 帧率是 "24000/1001" 这样的分数串。avg 是全片平均（变帧率的片子按它才诚实），
	// r 是基准值；avg 对某些流是 "0/0"，那时回落到 r。
	AvgFrameRate string `json:"avg_frame_rate"`
	RFrameRate   string `json:"r_frame_rate"`
	BitRate      string `json:"bit_rate"` // 十进制字符串；MKV 的流级码率常常缺失，兜底见 decide
	Disposition  struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
}

type probeOut struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
		Size     string `json:"size"`
	} `json:"format"`
}

// parseRate 解析 ffprobe 的分数帧率（"24000/1001" → 23.976）。
// 空串、"0/0"、除零一律返回 0 = 未知。
func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		return 0
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// Decision 是探测后的 HLS 播放决策（direct/unsupported 在 handler 层判定）。
//
// 探测结论与客户端无关，落库即长期有效。唯一与客户端有关的是 HEVC：能不能直出取决于
// 那台设备解不解，所以这里只记「这条流是几位的 HEVC」，到底 copy 还是重编码由
// CopyWith 在每次播放时定。
type Decision struct {
	VideoCopy bool // 视频流各家浏览器都可解 → -c:v copy（remux）；false = 看 VideoHEVC 或重编码
	VideoHEVC int  // 0=非 HEVC 或该 HEVC 不可直出；8=Main 8bit；10=Main 10bit
	AudioCopy bool // 音频流浏览器可解 → -c:a copy；false = 转 aac
	AudioAAC  bool // 音频编码为 aac：copy 进 fMP4 须挂 aac_adtstoasc（ADTS 源如 ts/m2ts 必需，ASC 源直通无害）
	HasVideo  bool
	HasAudio  bool
	Duration  float64 // 秒；<=0 = 未知

	// 以下是给人看的源规格（详情卡显示），不参与播放决策。空 / 0 = 未知，前端略过不显示。
	VideoCodec string  // ffprobe 的 codec_name 原值（h264/hevc/av1…），展示名由前端映射
	Width      int     // 像素
	Height     int     // 像素
	FPS        float64 // 帧率
	BitRate    int64   // bps

	videoTag string // -tag:v 值（HEVC 直出时为 hvc1），空 = 不加
}

// CopyWith 按客户端的 HEVC 能力（0=不支持 / 8=Main 8bit / 10=含 Main 10bit）定下这次
// 播放是否可直出。h264/vp9/av1 的结论与客户端无关，原样沿用。
//
// 判错的代价是不对称的：多转一次只是费 CPU，判成可直出而对方解不了就是黑屏。因此
// 能力由客户端自己 isTypeSupported 探出来上报，服务端不按 UA 猜。
func (d Decision) CopyWith(hevcCap int) Decision {
	if !d.VideoCopy && d.VideoHEVC > 0 && hevcCap >= d.VideoHEVC {
		d.VideoCopy = true
		// HEVC 进 fMP4 必须打 hvc1 标签：ffmpeg 默认写 hev1，Safari 与 MSE 都不认，
		// 表现为有声音没画面。
		d.videoTag = "hvc1"
	}
	return d
}

// httpInputArgs http(s) 输入的公共旗标（探测/抽帧/转码共用），本地路径输入返回 nil：
//   - X-Internal-Auth 头：标识本进程内部回环请求，服务器豁免下载限速、改走单流透传；
//   - reconnect 系列：流中断/限流(403/429/5xx)时带 Range 自动续传——没有它 ffmpeg 会把
//     半途断流当 EOF，remux 出"合法但截断"的片子；404 等不在列，删除的文件仍快速失败。
//
// 403 必须在列：它是 Google Drive 限流的主力返回码，一次限流就会让整场转码当场死掉。
// 真没权限时 ffmpeg 确实会重连到 -reconnect_delay_max 才罢休，但那种情形 raw 层自己
// 先重试并快速失败，轮不到这里空转。
func httpInputArgs(input, internalToken string) []string {
	if !strings.HasPrefix(input, "http") {
		return nil
	}
	a := []string{
		"-reconnect", "1", "-reconnect_streamed", "1",
		"-reconnect_delay_max", "30", "-reconnect_on_http_error", "403,429,5xx",
	}
	if internalToken != "" {
		a = append(a, "-headers", "X-Internal-Auth: "+internalToken+"\r\n")
	}
	return a
}

func runProbe(ctx context.Context, ffprobe, input, internalToken string) (*probeOut, error) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// -threads 1：读元数据不需要多核，默认值会让 ffprobe 占满全部核心；
	// 一屏封面同时探测就能把机器榨干（并发另有 Service.jobs 闸把关）。
	args := []string{"-hide_banner", "-v", "error", "-threads", "1"}
	args = append(args, httpInputArgs(input, internalToken)...)
	args = append(args, "-show_streams", "-show_format", "-of", "json", input)
	cmd := exec.CommandContext(cctx, ffprobe, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("读取视频信息失败: %w", err)
	}
	po := &probeOut{}
	if err := json.Unmarshal(out, po); err != nil {
		return nil, fmt.Errorf("解析视频信息失败: %w", err)
	}
	return po, nil
}

// 浏览器 MSE（fMP4 容器）可直接解码的编码。音频保守只认 aac/mp3
// （opus/flac 进 mp4 兼容性参差，转 aac 成本可忽略）；
// h264 仅 8bit 4:2:0（Hi10P 等浏览器不解）。
var playableAudio = map[string]bool{"aac": true, "mp3": true}

func playableVideoStream(st *probeStream) bool {
	switch st.CodecName {
	case "h264":
		return st.PixFmt == "" || st.PixFmt == "yuv420p" || st.PixFmt == "yuvj420p"
	case "vp9", "av1":
		return true
	}
	return false
}

// hevcLevel 判定这条 HEVC 流可直出所需的客户端能力：8=Main（8bit 4:2:0）、
// 10=Main 10（10bit 4:2:0）、0=不是 HEVC 或用了没人普遍支持的像素格式（4:2:2/4:4:4
// 与 12bit 一律重编码）。
//
// 只按像素格式判，不看 profile 字符串——理由同 h264 那条：ffprobe 报的 profile
// 名称各版本不一，pix_fmt 才是稳定的。
func hevcLevel(st *probeStream) int {
	if st.CodecName != "hevc" && st.CodecName != "h265" {
		return 0
	}
	switch st.PixFmt {
	case "", "yuv420p", "yuvj420p":
		return 8
	case "yuv420p10le":
		return 10
	}
	return 0
}

// decide 从 ffprobe 结果生成决策：取第一条视频流（跳过封面图）与第一条音频流。
func decide(po *probeOut) Decision {
	d := Decision{}
	if s := po.Format.Duration; s != "" {
		d.Duration, _ = strconv.ParseFloat(s, 64)
	}
	for i := range po.Streams {
		st := &po.Streams[i]
		switch {
		case st.CodecType == "video" && st.Disposition.AttachedPic == 0 && !d.HasVideo:
			d.HasVideo = true
			d.VideoCopy = playableVideoStream(st)
			if !d.VideoCopy {
				d.VideoHEVC = hevcLevel(st)
			}
			d.VideoCodec = st.CodecName
			d.Width, d.Height = st.Width, st.Height
			if d.FPS = parseRate(st.AvgFrameRate); d.FPS == 0 {
				d.FPS = parseRate(st.RFrameRate)
			}
			d.BitRate, _ = strconv.ParseInt(st.BitRate, 10, 64)
		case st.CodecType == "audio" && !d.HasAudio:
			d.HasAudio = true
			d.AudioCopy = playableAudio[st.CodecName]
			d.AudioAAC = st.CodecName == "aac"
		}
	}
	// 码率兜底：MKV 一类容器不写流级码率，退到整体码率（含音频，偏高一点点，但回答
	// 「这片子多大码」够诚实）；再没有就按文件大小除时长估。三条都落空即 0 = 未知。
	if d.BitRate == 0 {
		d.BitRate, _ = strconv.ParseInt(po.Format.BitRate, 10, 64)
	}
	if d.BitRate == 0 && d.Duration > 0 {
		if sz, err := strconv.ParseInt(po.Format.Size, 10, 64); err == nil && sz > 0 {
			d.BitRate = int64(float64(sz) * 8 / d.Duration)
		}
	}
	return d
}
