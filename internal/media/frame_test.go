package media

// FrameAt 的两类输入：多帧的视频（要定位到某一秒）与单帧的静态图（不能定位）。
// 后者是缩略图服务给超大图片兜底的那条路（thumb.genImage → hugeImage 分支）。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFrameAtStillImage 静态图抽帧必须出图。
//
// 单帧输入上任何定位参数（含 -ss 0）都会把唯一那一帧丢掉，而 ffmpeg 退出码仍是 0——
// 失败只表现为「产物为空」。缩略图服务正是靠这条路给超过解码上限的大图出封面，
// 一旦有人把 -ss 加回去，大图就彻底没有封面，且预载每轮都算一次失败、退避重试到 6 小时。
func TestFrameAtStillImage(t *testing.T) {
	ffmpeg := LookTool("ffmpeg")
	if ffmpeg == "" {
		t.Skip("本机无 ffmpeg，跳过静态图抽帧测试")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "big.jpg")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=4096x4096", "-frames:v", "1", src).CombinedOutput(); err != nil {
		t.Fatalf("生成样图: %v %s", err, out)
	}

	out := filepath.Join(dir, "thumb.jpg")
	if err := FrameFirst(context.Background(), ffmpeg, src, out, 640); err != nil {
		t.Fatalf("静态图应能抽出一帧: %v", err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("产物不可用: %v", err)
	}
}

// TestFrameAtVideoSeeks 视频那条路仍按秒点定位，且首帧回退可用。
func TestFrameAtVideoSeeks(t *testing.T) {
	ffmpeg := LookTool("ffmpeg")
	if ffmpeg == "" {
		t.Skip("本机无 ffmpeg，跳过视频抽帧测试")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=5",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Fatalf("生成样片: %v %s", err, out)
	}

	// 3 秒处存在：走第一个偏移即成功
	out := filepath.Join(dir, "at3.jpg")
	if err := FrameAt(context.Background(), ffmpeg, src, out, 320, "", "3", "0"); err != nil {
		t.Fatalf("3s 处应能抽帧: %v", err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("产物不可用: %v", err)
	}

	// 定位超出时长：回退到首帧那一档仍要出图（短片走的就是这条）
	out2 := filepath.Join(dir, "fallback.jpg")
	if err := FrameAt(context.Background(), ffmpeg, src, out2, 320, "", "999", "0"); err != nil {
		t.Fatalf("超出时长应回退首帧: %v", err)
	}
	if st, err := os.Stat(out2); err != nil || st.Size() == 0 {
		t.Fatalf("回退产物不可用: %v", err)
	}
}
