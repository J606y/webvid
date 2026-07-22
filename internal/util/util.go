// Package util 收拢跨包复用的无依赖纯函数，消除各处的同实现拷贝。
package util

// BoolInt 把 bool 编码为 SQLite 存储用的 0/1（取代 index/user/media/server 各自的同名副本）。
func BoolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// JoinLogical 拼接以 "/" 为根的逻辑路径：dir 为根时避免出现 "//"。
// 取代 fs.joinPath / index.joinPath / server.joinLogical 三处同实现。
func JoinLogical(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// JoinRel 拼接以 "" 为根的相对路径（驱动内部相对路径）：dir 为空时直接返回 name。
// 取代 fs/transfer.joinRel 与 pikpak.pathJoin 两处同实现。
// 注意：onedrive.joinRel 语义不同（两端先 Trim("/")），不在此列。
func JoinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
