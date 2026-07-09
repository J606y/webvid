// Package public 嵌入前端构建产物（frontend 构建输出到 dist/）。
package public

import "embed"

//go:embed all:dist
var Dist embed.FS
