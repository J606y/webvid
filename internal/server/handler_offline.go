package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/fs"
	"newlist/internal/media"
	"newlist/internal/task"
	"newlist/internal/user"
	"newlist/internal/util"
)

// POST /api/fs/offline {urls[], dst_dir} —— 离线下载：每个 URL 建一个后台任务（offline 组），
// 服务器拉流写入目标目录。返回 task_ids，进度/取消/重试走统一任务接口。
func (s *Server) fsOffline(c *gin.Context) {
	var req struct {
		URLs   []string `json:"urls"`
		DstDir string   `json:"dst_dir"`
		Name   string   `json:"name"` // 可选：自定义文件名，仅单链接时生效
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DstDir == "" {
		Fail(c, 400, "urls/dst_dir 不能为空")
		return
	}
	dst, err := fs.NormPath(req.DstDir)
	if err != nil {
		fsError(c, err)
		return
	}
	u := getUser(c)
	if !s.fs.Caps(u, dst).Upload {
		Fail(c, 400, "目标目录所在存储不支持上传")
		return
	}

	// 先收集有效 URL（trim、http/https、host 非空）：任一非法即整单 400。
	var valid []string
	for _, raw := range req.URLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		pu, err := url.Parse(raw)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			Fail(c, 400, "仅支持 http/https 链接: "+raw)
			return
		}
		valid = append(valid, raw)
	}
	if len(valid) == 0 {
		Fail(c, 400, "urls 不能为空")
		return
	}
	// 自定义文件名仅单链接时生效，多链接忽略（防同名互相覆盖）。
	customName := ""
	if len(valid) == 1 {
		customName = req.Name
	}

	var taskIDs []string
	for _, raw := range valid {
		pu, _ := url.Parse(raw) // valid 里已校验过，不会出错
		display := path.Base(pu.Path)
		if display == "" || display == "/" || display == "." {
			display = pu.Host
		}
		srcURL, cn := raw, customName // 闭包取副本
		t := s.tasks.SubmitIn(task.GroupOffline, u.ID, "离线下载 "+display+" → "+dst,
			func(ctx context.Context, t *task.Task) error {
				return s.offlineFetch(ctx, u, t, srcURL, dst, cn)
			})
		taskIDs = append(taskIDs, t.ID)
	}
	OK(c, gin.H{"task_ids": taskIDs})
}

// offlineClient 离线下载专用 HTTP 客户端：跟随重定向（限跳数），仅限连接阶段超时（下载本身不限时）。
// DialContext 挂 safeControl 逐跳拦内网/保留地址，防 SSRF 打内网或云元数据端点。
var offlineClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           offlineDialer().DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("重定向次数过多")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("重定向到不支持的协议: %s", req.URL.Scheme)
		}
		return nil
	},
}

// offlineFetch 拉取 URL 写入目标目录。文件名优先自定义名，其次响应 Content-Disposition，
// 再次 URL 末段；拉流共享全站下载限速。源未给 Content-Length 时进度只涨字节数，完成后补齐总量。
// 识别到 HLS（m3u8）则改交 ffmpeg 拉全部分片合并成 mp4（见 offlineFetchHLS）。
func (s *Server) offlineFetch(ctx context.Context, u *user.User, t *task.Task, srcURL, dstDir, customName string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return err
	}
	resp, err := offlineClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("源站返回 HTTP %d", resp.StatusCode)
	}

	// 先行 GET 已用 offlineClient 校验入口 host 是公网；若是 HLS 播放列表，
	// 别把列表文本当文件存——立即释放连接，交给 ffmpeg 重新拉分片合并。
	if isHLS(resp, srcURL) {
		resp.Body.Close()
		return s.offlineFetchHLS(ctx, u, t, srcURL, dstDir, customName)
	}

	name := sanitizeName(customName)
	if name == "" {
		name = offlineFilename(resp, srcURL)
	}
	t.SetFile(name)
	if resp.ContentLength > 0 {
		t.SetTotal(resp.ContentLength)
	}
	pr := &offlineProgressReader{r: resp.Body, t: t}
	r := s.limDown.Reader(ctx, pr)
	if err := s.fs.Put(ctx, u, dstDir, name, r, resp.ContentLength, false); err != nil {
		return err
	}
	if resp.ContentLength <= 0 {
		t.SetTotal(pr.n) // 源未报大小：以实收字节数收尾，避免完成时进度归零
	}
	target := util.JoinLogical(dstDir, name)
	if fi, err := s.fs.Get(ctx, u, target); err == nil {
		s.index.Upsert(target, fi)
	}
	return nil
}

// offlineFilename 从响应头/URL 提取并清洗文件名。
func offlineFilename(resp *http.Response, srcURL string) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if f := sanitizeName(params["filename"]); f != "" {
				return f
			}
		}
	}
	if pu, err := url.Parse(srcURL); err == nil {
		base := path.Base(pu.Path)
		if dec, derr := url.PathUnescape(base); derr == nil {
			base = dec
		}
		if f := sanitizeName(base); f != "" {
			return f
		}
	}
	return "download.bin"
}

// isHLS 判定响应是否为 HLS 播放列表：Content-Type 含 mpegurl（apple/x-mpegurl 等），
// 或 URL 路径以 .m3u8 结尾。命中则该"文件"其实是分片目录，须交 ffmpeg 拉取合并。
func isHLS(resp *http.Response, srcURL string) bool {
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") {
		return true
	}
	if pu, err := url.Parse(srcURL); err == nil {
		return strings.HasSuffix(strings.ToLower(pu.Path), ".m3u8")
	}
	return false
}

// hlsOutName 生成 HLS 合并产物的落地名：自定义名优先并强制补 .mp4；
// 否则取 URL 末段去扩展名（去掉 .m3u8），兜底 video —— 结果恒以 .mp4 结尾。
func hlsOutName(custom, srcURL string) string {
	if c := sanitizeName(custom); c != "" {
		if !strings.HasSuffix(strings.ToLower(c), ".mp4") {
			c += ".mp4"
		}
		return c
	}
	name := "video"
	if pu, err := url.Parse(srcURL); err == nil {
		b := path.Base(pu.Path)
		if dec, e := url.PathUnescape(b); e == nil {
			b = dec
		}
		b = strings.TrimSuffix(b, path.Ext(b)) // 去掉 .m3u8
		if s := sanitizeName(b); s != "" {
			name = s
		}
	}
	return name + ".mp4"
}

// offlineFetchHLS 用 ffmpeg 把 HLS 全部分片拉取并无损 remux 成单个 mp4 落入目标目录。
// 临时文件放数据盘（勿用 /tmp，可能是 tmpfs），完成即删；ctx 取消即杀 ffmpeg 并清理临时文件。
// -c copy 零重编码，-bsf:a aac_adtstoasc 把 ADTS AAC 转 ASC 进 fMP4（ASC 源直通无害，保证有声音）。
func (s *Server) offlineFetchHLS(ctx context.Context, u *user.User, t *task.Task, srcURL, dstDir, customName string) error {
	ffmpeg := media.LookTool("ffmpeg")
	if ffmpeg == "" {
		return media.ErrNoFFmpeg
	}

	name := hlsOutName(customName, srcURL)
	t.SetFile(name)

	dataDir := os.Getenv("NL_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	tmpDir := filepath.Join(dataDir, "offline-tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "hls-*.mp4")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	args := []string{
		"-hide_banner", "-loglevel", "error",
		// reconnect 系列：源站中途 429/5xx 带 Range 自动续传，避免半途断流被当 EOF 出截断文件。
		"-reconnect", "1", "-reconnect_streamed", "1",
		"-reconnect_delay_max", "30", "-reconnect_on_http_error", "429,5xx",
		// 白名单不含 file，畸形 m3u8 里的 file:// 引用被拒，防读本地文件。
		"-protocol_whitelist", "crypto,data,http,https,tcp,tls",
		"-i", srcURL,
		"-c", "copy", "-bsf:a", "aac_adtstoasc",
		"-progress", "pipe:1", "-nostats",
		"-y", tmpPath,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...) // ctx 取消即杀 ffmpeg
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	// 解析 -progress 的 total_size= 增量上报（未知总量，跟"源未报大小"一致，进度只涨字节）。
	sc := bufio.NewScanner(stdout)
	var last int64
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "total_size="); ok {
			if n, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64); e == nil && n > last {
				t.Add(n - last)
				last = n
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg 拉取 HLS 失败: %v: %s", err, strings.TrimSpace(stderr.String()))
	}

	// 落地：临时 mp4 → fs.Put（本地与云盘驱动共用同一路径）。
	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	t.SetTotal(fi.Size()) // 收尾补齐总量 → 进度条到 100%
	if err := s.fs.Put(ctx, u, dstDir, name, f, fi.Size(), false); err != nil {
		return err
	}

	target := util.JoinLogical(dstDir, name)
	if gi, err := s.fs.Get(ctx, u, target); err == nil {
		s.index.Upsert(target, gi)
	}
	return nil
}

// sanitizeName 去掉路径分隔与控制字符，剩空则返回 ""。
func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '/' || r == '\\' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// offlineProgressReader 读取时上报任务进度并累计字节数。
type offlineProgressReader struct {
	r io.Reader
	t *task.Task
	n int64
}

func (p *offlineProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		p.t.Add(int64(n))
	}
	return n, err
}
