package main

import (
	"context"
	"encoding/json"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/db"
	_ "newlist/internal/driver/local"    // 注册本地驱动
	_ "newlist/internal/driver/onedrive" // 注册 onedrive / onedrive_app 驱动
	_ "newlist/internal/driver/pikpak"   // 注册 pikpak 驱动
	_ "newlist/internal/driver/telegram" // 注册 telegram 收藏夹驱动
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/preload"
	"newlist/internal/server"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

func init() {
	// Go 默认 mime 表不含 .webmanifest，会以 text/plain 下发，个别 iOS 因此忽略 PWA 清单。
	// 显式注册为标准类型，确保「添加到主屏幕」以独立 App 方式运行。
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	// 管理子命令（如 reset-password）不启动服务，处理完直接退出。
	if runCLI() {
		return
	}

	port := env("NL_PORT", "5243")
	dataDir := env("NL_DATA_DIR", "./data")
	filesDir := env("NL_FILES_DIR", "./files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		log.Fatalf("创建文件目录失败: %v", err)
	}

	d, err := db.Open(dataDir)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	cf, err := conf.New(d)
	if err != nil {
		log.Fatalf("读取设置失败: %v", err)
	}
	secret, err := cf.JWTSecret()
	if err != nil {
		log.Fatalf("初始化 JWT 密钥失败: %v", err)
	}

	users := user.NewStore(d)
	if n, err := users.Count(); err != nil {
		log.Fatalf("查询用户失败: %v", err)
	} else if n == 0 {
		name := env("NL_ADMIN_USER", "admin")
		pw := os.Getenv("NL_ADMIN_PASSWORD")
		if pw == "" {
			pw = auth.RandomPassword(12)
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			log.Fatalf("初始化管理员失败: %v", err)
		}
		if _, err := users.Create(name, hash, "admin", "/", true); err != nil {
			log.Fatalf("创建管理员失败: %v", err)
		}
		log.Printf("\n"+
			"==================================\n"+
			"  初始管理员账号\n"+
			"  用户名: %s\n"+
			"  密  码: %s\n"+
			"  （仅本次显示，请登录后修改）\n"+
			"==================================", name, pw)
	}

	// 首启自动挂载本地存储
	var storageCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM storages`).Scan(&storageCount); err != nil {
		log.Fatalf("查询存储失败: %v", err)
	}
	if storageCount == 0 {
		abs, err := filepath.Abs(filesDir)
		if err != nil {
			abs = filesDir
		}
		cfgJSON, _ := json.Marshal(map[string]string{"root_path": abs})
		if _, err := d.Exec(
			`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
			 VALUES('/本地存储', 'local', ?, 0, 1, '', ?)`,
			string(cfgJSON), time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Fatalf("初始化本地存储失败: %v", err)
		}
		log.Printf("已自动挂载本地存储: /本地存储 -> %s", abs)
	}

	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		log.Fatalf("加载存储失败: %v", err)
	}
	th := thumb.New(f, dataDir)
	md := media.New(f, dataDir, "http://127.0.0.1:"+port, secret, d)
	idx := index.New(d, f)
	// 索引就绪后后台预载：下载/生成封面 + 探测视频源信息写入 media_info。
	// 存储变更（含新挂载/勾选展示开关）→ Reload 重建索引 → 完成即触发本轮预载。
	pl := preload.New(d, f, th, md)
	idx.OnComplete(pl.Run)

	// 线程数/限速均来自 settings（后台可热调整）；离线下载组与限速器在 server.New 内接线
	srv := server.New(d, cf, users, f, th, md, idx, pl, task.New(cf.CopyWorkers()), secret)
	hs := &http.Server{
		Addr:    ":" + port,
		Handler: srv.Router(),
		// 不设 Read/WriteTimeout：大文件上传下载是长连接
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 先绑定监听端口再触发预载：预载探测云盘视频经本机 /api/raw 回环，
	// 需服务已在接受连接（否则首批探测连接被拒）。
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("监听端口失败: %v", err)
	}
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()
	log.Printf("WebVid %s 已启动: http://localhost:%s", conf.Version, port)

	// files 表为空且有可用存储 → 自动后台建索引（完成后经 OnComplete 触发预载）；
	// 索引已存在则直接跑一轮预载，补齐历史未缓存的封面/源信息（已缓存的快速跳过）。
	var fileCount int
	d.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&fileCount)
	if len(f.Mounts()) > 0 {
		if fileCount == 0 {
			idx.Rebuild()
		} else {
			pl.Run()
		}
	}

	<-ctx.Done()
	log.Println("正在关闭…")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(sctx)
	md.Close() // 终止在跑的 ffmpeg，清理转码临时目录，避免孤儿进程残留
}
