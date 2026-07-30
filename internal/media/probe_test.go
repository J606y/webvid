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
			Decision{VideoCopy: true, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 120.5}},
		{"h264+dts 只转音频", []probeStream{vstream("h264", "yuv420p"), astream("dts")}, "60",
			Decision{VideoCopy: true, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60}},
		{"h264+ac3 只转音频", []probeStream{vstream("h264", "yuv420p"), astream("ac3")}, "60",
			Decision{VideoCopy: true, HasVideo: true, HasAudio: true, Duration: 60}},
		{"h265 8bit 记为可直出候选", []probeStream{vstream("hevc", "yuv420p"), astream("aac")}, "60",
			Decision{VideoCopy: false, VideoHEVC: 8, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60}},
		{"h265 10bit 记为可直出候选", []probeStream{vstream("hevc", "yuv420p10le"), astream("aac")}, "60",
			Decision{VideoCopy: false, VideoHEVC: 10, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60}},
		{"h265 4:2:2 只能转码", []probeStream{vstream("hevc", "yuv422p10le"), astream("aac")}, "60",
			Decision{VideoCopy: false, VideoHEVC: 0, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60}},
		{"Hi10P h264 全转码", []probeStream{vstream("h264", "yuv420p10le"), astream("aac")}, "60",
			Decision{VideoCopy: false, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60}},
		{"wmv3+wmav2 全转码", []probeStream{vstream("wmv3", "yuv420p"), astream("wmav2")}, "60",
			Decision{VideoCopy: false, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60}},
		{"rv40+cook 全转码", []probeStream{vstream("rv40", "yuv420p"), astream("cook")}, "60",
			Decision{VideoCopy: false, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60}},
		{"vp9 remux", []probeStream{vstream("vp9", "yuv420p"), astream("opus")}, "60",
			Decision{VideoCopy: true, AudioCopy: false, HasVideo: true, HasAudio: true, Duration: 60}},
		{"跳过封面图流取真视频流", []probeStream{
			func() probeStream {
				st := vstream("mjpeg", "yuvj420p")
				st.Disposition.AttachedPic = 1
				return st
			}(), vstream("h264", "yuv420p"), astream("mp3")}, "60",
			Decision{VideoCopy: true, AudioCopy: true, HasVideo: true, HasAudio: true, Duration: 60}},
		{"纯音频", []probeStream{astream("flac")}, "60",
			Decision{HasAudio: true, Duration: 60}},
		{"无时长字段", []probeStream{vstream("mpeg4", "yuv420p"), astream("mp3")}, "",
			Decision{VideoCopy: false, AudioCopy: true, HasVideo: true, HasAudio: true, Duration: 0}},
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
