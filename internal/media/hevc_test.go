package media

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestHevcDirectPlay HEVC 直出：解得动的设备原样封装（零编码开销），解不动的照旧重编码。
//
// 这条是整个 HEVC 改动的命门。判成可直出而设备解不了就是黑屏，所以这里不只看决策，
// 还把产出的分片拼回去交给 ffprobe —— 必须仍是 hevc，才说明真的 copy 了，
// 也才说明 -tag:v hvc1 没把封装写坏。
func TestHevcDirectPlay(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/hevc.mkv"

	// 不支持 HEVC 的客户端：libx264 重编码，走服务端 VOD 列表
	if _, err := svc.Playlist(ctx, u, p, 0); err != nil {
		t.Fatalf("Playlist(不支持 HEVC): %v", err)
	}
	enc := svc.sessions[sessionKey(p, 0)]
	if enc == nil {
		t.Fatal("未建立会话")
	}
	if !enc.vod || enc.dec.VideoCopy || enc.dec.VideoHEVC != 8 {
		t.Fatalf("不支持 HEVC 的客户端应走重编码 VOD 会话，实际 %+v", enc.dec)
	}
	if enc.dec.videoTag != "" {
		t.Fatalf("重编码不该带 -tag:v，实际 %q", enc.dec.videoTag)
	}

	// 解得动的客户端：原样封装，走 ffmpeg 自写的 event 列表
	if _, err := svc.Playlist(ctx, u, p, 8); err != nil {
		t.Fatalf("Playlist(支持 HEVC): %v", err)
	}
	cp := svc.sessions[sessionKey(p, 8)]
	if cp == nil {
		t.Fatal("未建立直出会话")
	}
	if cp.vod || !cp.dec.VideoCopy || cp.dec.videoTag != "hvc1" {
		t.Fatalf("支持 HEVC 的客户端应走 hvc1 直出会话，实际 %+v", cp.dec)
	}
	// 两种会话产出的分片内容完全不同，目录必须分开，否则互相盖写
	if cp.dir == enc.dir {
		t.Fatalf("直出与重编码会话共用了目录 %s", cp.dir)
	}

	initFp, err := svc.Segment(ctx, u, p, "init.mp4", 8)
	if err != nil {
		t.Fatalf("init.mp4: %v", err)
	}
	segFp, err := svc.Segment(ctx, u, p, "seg_0.m4s", 8)
	if err != nil {
		t.Fatalf("seg_0.m4s: %v", err)
	}
	if got := concatVideoCodec(t, initFp, segFp); got != "hevc" {
		t.Fatalf("直出分片的视频编码应仍为 hevc（说明是 copy 而非偷偷重编码），实际 %q", got)
	}
}

// TestHevc10bitNeedsCapableClient 10bit 要求客户端也报得出 10bit，只报 8bit 不给直出。
func TestHevc10bitNeedsCapableClient(t *testing.T) {
	d := Decision{VideoHEVC: 10, HasVideo: true, Duration: 10}
	if d.CopyWith(8).VideoCopy {
		t.Fatal("只支持 8bit 的客户端不该拿到 10bit 直出")
	}
	if !d.CopyWith(10).VideoCopy {
		t.Fatal("支持 10bit 的客户端应拿到直出")
	}
}

// concatVideoCodec 把 init 与分片拼起来，返回 ffprobe 报的视频编码名。
func concatVideoCodec(t *testing.T, initFile, segFile string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "concat.mp4")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{initFile, segFile} {
		in, err := os.Open(fp)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(out, in)
		in.Close()
	}
	out.Close()
	po, err := runProbe(context.Background(), LookTool("ffprobe"), tmp, "")
	if err != nil {
		t.Fatalf("ffprobe 拼接分片失败: %v", err)
	}
	for i := range po.Streams {
		if po.Streams[i].CodecType == "video" {
			return po.Streams[i].CodecName
		}
	}
	return ""
}
