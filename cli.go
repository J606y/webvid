package main

import (
	"fmt"
	"log"
	"os"

	"newlist/internal/auth"
	"newlist/internal/db"
	"newlist/internal/user"
)

// runCLI 处理不启动 HTTP 服务的管理子命令；返回 true 表示已处理，main 应直接退出。
// 目前支持：
//
//	webvid reset-password [新密码]   重置管理员密码（省略则随机生成），不动存储/其他用户
func runCLI() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "reset-password", "reset-passwd":
		resetPassword(os.Args[2:])
		return true
	default:
		return false
	}
}

// resetPassword 重置管理员密码。密码优先级：命令行参数 > NL_ADMIN_PASSWORD > 随机生成。
// 只改 users 表里管理员那一行的 password_hash，不触碰存储配置/JWT 密钥/其他用户，
// 因此比「删库重置」安全得多。运行前应先停服务，避免与在跑实例争 SQLite 写锁。
func resetPassword(args []string) {
	dataDir := env("NL_DATA_DIR", "./data")
	d, err := db.Open(dataDir)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	users := user.NewStore(d)
	name := env("NL_ADMIN_USER", "admin")
	u, err := users.GetByUsername(name)
	if err != nil {
		// 用户名被改过：回退到第一个管理员
		list, lerr := users.List()
		if lerr != nil {
			log.Fatalf("查询用户失败: %v", lerr)
		}
		for _, x := range list {
			if x.IsAdmin() {
				u = x
				break
			}
		}
		if u == nil {
			log.Fatalf("未找到管理员用户（数据库可能尚未初始化，请先正常启动一次）")
		}
	}

	pw := ""
	switch {
	case len(args) > 0 && args[0] != "":
		pw = args[0]
	case os.Getenv("NL_ADMIN_PASSWORD") != "":
		pw = os.Getenv("NL_ADMIN_PASSWORD")
	default:
		pw = auth.RandomPassword(12)
	}

	hash, err := auth.HashPassword(pw)
	if err != nil {
		log.Fatalf("生成密码哈希失败: %v", err)
	}
	if err := users.UpdatePassword(u.ID, hash); err != nil {
		log.Fatalf("更新密码失败: %v", err)
	}
	fmt.Printf("\n"+
		"==================================\n"+
		"  管理员密码已重置\n"+
		"  用户名: %s\n"+
		"  新密码: %s\n"+
		"==================================\n", u.Username, pw)
}
