package media

import "testing"

func vstream(codec, pixfmt string) probeStream {
	st := probeStream{CodecType: "video", CodecName: codec, PixFmt: pixfmt}
	return st
}

func astream(codec string) probeStream {
	return probeStream{CodecType: "audio", CodecName: codec}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		streams  []probeStream
		duration string
		want     Decision
	}{
		{"mkv h264+aac 纯 remux", []probeStream{vstream("h264", "yuv420p"), astream("aac")}, "120.5",
			Decision{VideoCopy: true, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 120.5, VideoCodec: "h264"}},
		{"h264+dts 只转音频", []probeStream{vstream("h264", "yuv420p"), astream("dts")}, "60",
			Decision{VideoCopy: true, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "h264"}},
		{"h264+ac3 只转音频", []probeStream{vstream("h264", "yuv420p"), astream("ac3")}, "60",
			Decision{VideoCopy: true, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "h264"}},
		{"h265 8bit 记为可直出候选", []probeStream{vstream("hevc", "yuv420p"), astream("aac")}, "60",
			Decision{VideoCopy: false, VideoHEVC: 8, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "hevc"}},
		{"h265 10bit 记为可直出候选", []probeStream{vstream("hevc", "yuv420p10le"), astream("aac")}, "60",
			Decision{VideoCopy: false, VideoHEVC: 10, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "hevc"}},
		{"h265 4:2:2 只能转码", []probeStream{vstream("hevc", "yuv422p10le"), astream("aac")}, "60",
			Decision{VideoCopy: false, VideoHEVC: 0, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "hevc"}},
		{"Hi10P h264 全转码", []probeStream{vstream("h264", "yuv420p10le"), astream("aac")}, "60",
			Decision{VideoCopy: false, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "h264"}},
		{"wmv3+wmav2 全转码", []probeStream{vstream("wmv3", "yuv420p"), astream("wmav2")}, "60",
			Decision{VideoCopy: false, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "wmv3"}},
		{"rv40+cook 全转码", []probeStream{vstream("rv40", "yuv420p"), astream("cook")}, "60",
			Decision{VideoCopy: false, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "rv40"}},
		{"vp9 remux", []probeStream{vstream("vp9", "yuv420p"), astream("opus")}, "60",
			Decision{VideoCopy: true, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "vp9"}},
		{"跳过封面图流取真视频流", []probeStream{
			func() probeStream {
				st := vstream("mjpeg", "yuvj420p")
				st.Disposition.AttachedPic = 1
				return st
			}(), vstream("h264", "yuv420p"), astream("mp3")}, "60",
			// 规格也必须取自真视频流：VideoCodec 记成 mjpeg 就说明封面图没被跳过
			Decision{VideoCopy: true, AudioCopy: true, HasVideo: true, HasAudio: true, Duration: 60, VideoCodec: "h264"}},
		{"纯音频", []probeStream{astream("flac")}, "60",
			Decision{HasAudio: true, Duration: 60}},
		{"无时长字段", []probeStream{vstream("mpeg4", "yuv420p"), astream("mp3")}, "",
			Decision{VideoCopy: false, AudioCopy: true, HasVideo: true, HasAudio: true, Duration: 0, VideoCodec: "mpeg4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			po := &probeOut{Streams: tc.streams}
			po.Format.Duration = tc.duration
			got := decide(po)
			if got != tc.want {
				t.Fatalf("decide() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseRate 帧率是 "24000/1001" 这样的分数串，畸形值一律按未知处理 ——
// 详情卡宁可不显示这一项，也不能显示 NaN 或 Infinity。
func TestParseRate(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"25/1", 25},
		{"24000/1001", 23.976023976023978},
		{"0/0", 0},   // ffprobe 对某些流就是这么报的
		{"30/0", 0},  // 除零不能放出 Infinity
		{"", 0},      // 字段缺失
		{"25", 0},    // 没有分隔符
		{"a/b", 0},   // 非数字
		{"0/1", 0},   // 真的是 0 帧率
	}
	for _, tc := range cases {
		if got := parseRate(tc.in); got != tc.want {
			t.Errorf("parseRate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestDecideSpecs 源规格的提取与码率兜底：流级码率优先，缺失退到整体码率，
// 再缺失按 文件大小×8÷时长 估算 —— MKV 不写流级码率是常态，不能就此空着。
func TestDecideSpecs(t *testing.T) {
	mk := func(streamBR, formatBR, size, dur string, avg, r string) Decision {
		v := vstream("h264", "yuv420p")
		v.Width, v.Height = 1920, 1080
		v.AvgFrameRate, v.RFrameRate = avg, r
		v.BitRate = streamBR
		po := &probeOut{Streams: []probeStream{v}}
		po.Format.Duration, po.Format.BitRate, po.Format.Size = dur, formatBR, size
		return decide(po)
	}

	d := mk("8000000", "9000000", "1000000", "60", "24000/1001", "24/1")
	if d.Width != 1920 || d.Height != 1080 {
		t.Errorf("分辨率 = %dx%d, want 1920x1080", d.Width, d.Height)
	}
	if d.BitRate != 8000000 {
		t.Errorf("流级码率在时应优先采用, got %d", d.BitRate)
	}
	if d.FPS != 23.976023976023978 {
		t.Errorf("应取 avg_frame_rate, got %v", d.FPS)
	}

	// avg 为 "0/0"（ffprobe 对部分流的实际输出）→ 回落 r_frame_rate
	if d := mk("", "", "", "60", "0/0", "25/1"); d.FPS != 25 {
		t.Errorf("avg 无效时应回落 r_frame_rate, got %v", d.FPS)
	}
	// 流级缺失 → 整体码率
	if d := mk("", "9000000", "1000000", "60", "25/1", "25/1"); d.BitRate != 9000000 {
		t.Errorf("流级缺失应退到整体码率, got %d", d.BitRate)
	}
	// 两级都缺 → 按大小与时长估（1000000 B × 8 ÷ 60s）
	if d := mk("", "", "1000000", "60", "25/1", "25/1"); d.BitRate != 133333 {
		t.Errorf("应按大小与时长估算, got %d", d.BitRate)
	}
	// 时长为 0 时不能拿它做除数
	if d := mk("", "", "1000000", "", "25/1", "25/1"); d.BitRate != 0 {
		t.Errorf("无时长时码率应为未知, got %d", d.BitRate)
	}
}

// TestCopyWith HEVC 直出只在客户端解得动时才成立。
// 判错的代价不对称：多转一次只是费 CPU，判成可直出而对方解不了就是黑屏。
func TestCopyWith(t *testing.T) {
	hevc8 := Decision{VideoHEVC: 8, HasVideo: true}
	hevc10 := Decision{VideoHEVC: 10, HasVideo: true}
	h264 := Decision{VideoCopy: true, HasVideo: true}
	other := Decision{HasVideo: true} // 既非 HEVC 也不可直出

	cases := []struct {
		name     string
		in       Decision
		cap      int
		wantCopy bool
		wantTag  string
	}{
		{"8bit 遇不支持的客户端", hevc8, 0, false, ""},
		{"8bit 遇 8bit 客户端", hevc8, 8, true, "hvc1"},
		{"8bit 遇 10bit 客户端", hevc8, 10, true, "hvc1"},
		{"10bit 遇 8bit 客户端", hevc10, 8, false, ""},
		{"10bit 遇 10bit 客户端", hevc10, 10, true, "hvc1"},
		{"h264 不受能力影响", h264, 0, true, ""},
		{"其它编码仍要转码", other, 10, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.CopyWith(tc.cap)
			if got.VideoCopy != tc.wantCopy || got.videoTag != tc.wantTag {
				t.Fatalf("CopyWith(%d) = copy:%v tag:%q，期望 copy:%v tag:%q",
					tc.cap, got.VideoCopy, got.videoTag, tc.wantCopy, tc.wantTag)
			}
		})
	}
}
