# WebVid 构建入口
#
# 关键约束（见 docs/PROGRESS.md 踩坑记录）：曾经用
# `npm run build 2>&1 | tail -N && go build ...` 部署，管道退出码取自 tail 而非 vite，
# 前端构建失败也会被当成成功，go build 照样把 go:embed 指向的旧 public/dist 静默嵌进二进制，
# 结果线上跑的是过期前端。因此下面每个目标都**不接管道/不吞退出码**，直接依赖命令自身的
# 退出状态；make 默认没有 -k/.IGNORE，任一命令非零退出会立即中止整条依赖链
# （frontend 失败 ⇒ build 不会继续跑 go build）。

.PHONY: all frontend build build-linux test vet clean

all: build

# 前端构建产物落 public/dist（frontend/vite.config.js 里的 outDir），随后被 main 包
# go:embed 进二进制。这里直接跑 npm，不做任何重定向/管道包装，失败即让 make 整体失败。
frontend:
	npm --prefix frontend run build

# Windows 产物：webvid.exe。依赖 frontend，保证嵌入的是刚构建出的前端，不是旧 dist。
build: frontend
	go build -trimpath -ldflags "-s -w" -o webvid.exe .

# Linux/macOS 产物：webvid（无扩展名）。交叉编译示例：
#   GOOS=linux GOARCH=amd64 make build-linux
build-linux: frontend
	go build -trimpath -ldflags "-s -w" -o webvid .

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f webvid webvid.exe
	rm -rf public/dist
