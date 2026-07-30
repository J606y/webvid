package media

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestPaceStopsRunawayTranscode 转码领先播放位置太多就停下，播放位置追上来再原地续转。
//
// 不设上限时 ffmpeg 会全速把整部片子转完：用户看了五分钟就退出，CPU 与云盘流量照样按
// 整片付账。停下来不能影响观看，所以这里还要验它能续得回来。
func TestPaceStopsRunawayTranscode(t *testing.T) {
	old := maxLead
	maxLead = 2
	defer func() { maxLead = old }()

	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/long.mkv" // 60 秒 = 15 个分片，mpeg4 → 必然重编码走 vod

	if _, err := svc.Playlist(ctx, u, p, 0); err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	sess := svc.sessions[sessionKey(p, 0)]
	if sess == nil || !sess.vod {
		t.Fatalf("期望 vod 重编码会话，实际 %+v", sess)
	}

	// 等它跑到领先 maxLead 以上（只要 cursor 过了 seg_0 + maxLead 就够判）
	deadline := time.Now().Add(30 * time.Second)
	for {
		sess.mu.Lock()
		sess.advanceCursorLocked()
		cursor, running := sess.cursor, sess.cmd != nil
		sess.mu.Unlock()
		if cursor > maxLead {
			break
		}
		if !running {
			t.Fatalf("ffmpeg 提前退出，cursor=%d", cursor)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待转码领先超时，cursor=%d", cursor)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 请求一个早已生成的分片：领先太多，这一下应该把 ffmpeg 停掉
	if _, err := svc.Segment(ctx, u, p, "seg_0.m4s", 0); err != nil {
		t.Fatalf("seg_0.m4s: %v", err)
	}
	sess.mu.Lock()
	stopped := sess.cmd == nil
	sess.mu.Unlock()
	if !stopped {
		t.Fatal("转码领先超过上限后应停下，实际仍在跑")
	}

	// 停下不等于播不了：请求尚未生成的分片应原地 -ss 续转并等到产物
	fp, err := svc.Segment(ctx, u, p, "seg_14.m4s", 0)
	if err != nil {
		t.Fatalf("停下后应能续转出 seg_14: %v", err)
	}
	if !ready(fp) {
		t.Fatalf("续转产物不可用: %s", fp)
	}
}

// TestTranscodeArgsThreads 重编码必须带 -threads，否则 libx264 按核数开满，一路转码吃光整机。
// 直出分支不该带（copy 不编码），但 HEVC 直出必须带 -tag:v hvc1。
func TestTranscodeArgsThreads(t *testing.T) {
	enc := &session{svc: &Service{}, input: "/tmp/a.mkv", vod: true,
		dec: Decision{HasVideo: true, HasAudio: true}}
	args := strings.Join(enc.ffmpegArgs(0), " ")
	if !strings.Contains(args, "-threads ") {
		t.Fatalf("重编码缺 -threads:\n%s", args)
	}
	if !strings.Contains(args, "libx264") {
		t.Fatalf("期望 libx264 分支:\n%s", args)
	}

	cp := &session{svc: &Service{}, input: "/tmp/a.mkv",
		dec: Decision{HasVideo: true, HasAudio: true, AudioCopy: true, VideoCopy: true}}
	if got := strings.Join(cp.ffmpegArgs(0), " "); strings.Contains(got, "-tag:v") {
		t.Fatalf("非 HEVC 直出不该带 -tag:v:\n%s", got)
	}

	hevc := &session{svc: &Service{}, input: "/tmp/a.mkv",
		dec: Decision{VideoHEVC: 8, HasVideo: true, HasAudio: true, AudioCopy: true}.CopyWith(8)}
	got := strings.Join(hevc.ffmpegArgs(0), " ")
	if !strings.Contains(got, "-c:v copy") || !strings.Contains(got, "-tag:v hvc1") {
		t.Fatalf("HEVC 直出应为 copy + hvc1 标签:\n%s", got)
	}
}
