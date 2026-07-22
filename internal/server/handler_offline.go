package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/fs"
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

	var taskIDs []string
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
		display := path.Base(pu.Path)
		if display == "" || display == "/" || display == "." {
			display = pu.Host
		}
		srcURL := raw // 闭包取副本
		t := s.tasks.SubmitIn(task.GroupOffline, u.ID, "离线下载 "+display+" → "+dst,
			func(ctx context.Context, t *task.Task) error {
				return s.offlineFetch(ctx, u, t, srcURL, dst)
			})
		taskIDs = append(taskIDs, t.ID)
	}
	if len(taskIDs) == 0 {
		Fail(c, 400, "urls 不能为空")
		return
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

// offlineFetch 拉取 URL 写入目标目录。文件名优先响应的 Content-Disposition，其次 URL 末段；
// 拉流共享全站下载限速。源未给 Content-Length 时进度只涨字节数，完成后补齐总量。
func (s *Server) offlineFetch(ctx context.Context, u *user.User, t *task.Task, srcURL, dstDir string) error {
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

	name := offlineFilename(resp, srcURL)
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
