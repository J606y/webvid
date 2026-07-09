package model

import (
	"path"
	"strings"
	"time"
)

// FileInfo 是对外统一的文件条目 DTO。
type FileInfo struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified time.Time `json:"modified"`
}

var videoExts = map[string]bool{
	"mp4": true, "mkv": true, "avi": true, "mov": true, "wmv": true,
	"flv": true, "webm": true, "m4v": true, "ts": true, "m2ts": true,
	"rmvb": true, "rm": true, "mpg": true, "mpeg": true, "vob": true, "3gp": true,
}

var imageExts = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true,
	"bmp": true, "avif": true, "heic": true,
}

// Ext 返回小写、不带点的扩展名。
func Ext(name string) string {
	e := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	return e
}

// ExtType 按扩展名归类：video / image / other（索引与媒体库共用）。
func ExtType(name string) string {
	e := Ext(name)
	switch {
	case videoExts[e]:
		return "video"
	case imageExts[e]:
		return "image"
	default:
		return "other"
	}
}
