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
		{"h265 全转码", []probeStream{vstream("hevc", "yuv420p"), astream("aac")}, "60",
			Decision{VideoCopy: false, AudioCopy: true, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 60}},
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
