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
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	PixFmt      string `json:"pix_fmt"`
	Disposition struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
}

type probeOut struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Decision 是探测后的 HLS 播放决策（direct/unsupported 在 handler 层判定）。
type Decision struct {
	VideoCopy bool // 视频流浏览器可解 → -c:v copy（remux）；false = libx264 重编码
	AudioCopy bool // 音频流浏览器可解 → -c:a copy；false = 转 aac
	AudioAAC  bool // 音频编码为 aac：copy 进 fMP4 须挂 aac_adtstoasc（ADTS 源如 ts/m2ts 必需，ASC 源直通无害）
	HasVideo  bool
	HasAudio  bool
	Duration  float64 // 秒；<=0 = 未知
}

// httpInputArgs http(s) 输入的公共旗标（探测/抽帧/转码共用），本地路径输入返回 nil：
//   - X-Internal-Auth 头：标识本进程内部回环请求，服务器豁免下载限速、改走单流透传；
//   - reconnect 系列：流中断/限流(429/5xx)时带 Range 自动续传——没有它 ffmpeg 会把
//     半途断流当 EOF，remux 出"合法但截断"的片子；404 等不在列，删除的文件仍快速失败。
func httpInputArgs(input, internalToken string) []string {
	if !strings.HasPrefix(input, "http") {
		return nil
	}
	a := []string{
		"-reconnect", "1", "-reconnect_streamed", "1",
		"-reconnect_delay_max", "30", "-reconnect_on_http_error", "429,5xx",
	}
	if internalToken != "" {
		a = append(a, "-headers", "X-Internal-Auth: "+internalToken+"\r\n")
	}
	return a
}

func runProbe(ctx context.Context, ffprobe, input, internalToken string) (*probeOut, error) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-v", "error"}
	args = append(args, httpInputArgs(input, internalToken)...)
	args = append(args, "-show_streams", "-show_format", "-of", "json", input)
	cmd := exec.CommandContext(cctx, ffprobe, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe 探测失败: %w", err)
	}
	po := &probeOut{}
	if err := json.Unmarshal(out, po); err != nil {
		return nil, fmt.Errorf("ffprobe 输出解析失败: %w", err)
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
		case st.CodecType == "audio" && !d.HasAudio:
			d.HasAudio = true
			d.AudioCopy = playableAudio[st.CodecName]
			d.AudioAAC = st.CodecName == "aac"
		}
	}
	return d
}
