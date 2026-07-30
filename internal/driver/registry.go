package driver

import "sort"

// FieldSpec 描述驱动的一个配置字段，前端按此动态渲染表单。
type FieldSpec struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // string | password | number | bool | select
	Required bool     `json:"required"`
	Default  string   `json:"default"`
	Options  []string `json:"options,omitempty"`
	Secret   bool     `json:"secret"` // 回显时脱敏为 ***
	Help     string   `json:"help,omitempty"`
	Locked   bool     `json:"locked,omitempty"` // 前端渲染为禁用：取值由驱动决定，用户改不动
}

// Meta 描述一个已注册驱动。
type Meta struct {
	Name   string      `json:"name"`
	Label  string      `json:"label"`
	Remote bool        `json:"remote"` // 远端存储：追加代理/加速等通用字段
	Fields []FieldSpec `json:"fields"`
	// AlwaysProxy 该驱动的流量恒经服务器转发，代理模式开关无意义：Register 会把 proxy
	// 字段换成锁定的「已开启」，fs 层也按 true 读（见 Mount.accelOpts / AlwaysProxy）。
	AlwaysProxy bool `json:"-"`
}

type factory func() Driver

var registry = map[string]struct {
	meta Meta
	fn   factory
}{}

// CommonRemoteFields 远端驱动统一追加的字段（fs 层读取，驱动可忽略）。
var CommonRemoteFields = []FieldSpec{
	// 标签只写四个字：表单的 label 宽度容不下带括号的长标签，会从括号中间裁断。
	{Name: "proxy", Label: "代理模式", Type: "bool", Default: "false",
		Help: "开启后下载/播放经服务器转发"},
	// 键名保留 threads/chunk_mb（已存进挂载配置，改键即丢值）；标签只讲用户能感觉到的事：
	// 缓冲多深。这个数字同时是窗口容量与并发连接数，见 stream.NewMultiReader。
	{Name: "threads", Label: "缓冲块数", Type: "number", Default: "4",
		Help: "缓冲大小 = 缓冲块数 × 每块大小"},
	{Name: "chunk_mb", Label: "每块大小(MB)", Type: "number", Default: "4",
		Help: "首次播放或拖动进度条需要加载完当前的一块，块越小首播和拖动越快，但相应的加载也越频繁"},
}

// CommonFields 所有驱动统一追加的字段（fs/server 层读取，驱动可忽略）。
var CommonFields = []FieldSpec{
	{Name: "show_video", Label: "在视频库展示", Type: "bool", Default: "true",
		Help: "关闭后此存储的视频不出现在视频库（文件管理不受影响）"},
	{Name: "show_photo", Label: "在照片墙展示", Type: "bool", Default: "true",
		Help: "关闭后此存储的图片不出现在照片墙（文件管理不受影响）"},
	{Name: "show_search", Label: "在搜索中展示", Type: "bool", Default: "true",
		Help: "关闭后此存储的全部内容不出现在搜索结果（文件管理不受影响）"},
}

// lockedProxyField 恒转发驱动的 proxy 字段：显示成已开启且改不动，别让用户以为关掉能省流量。
var lockedProxyField = FieldSpec{
	Name: "proxy", Label: "代理模式", Type: "bool", Default: "true", Locked: true,
	Help: "此存储不支持直链下发，下载/播放始终经服务器转发，无法关闭",
}

// Register 注册驱动；在驱动包的 init() 中调用。
func Register(meta Meta, fn factory) {
	if meta.Remote {
		common := CommonRemoteFields
		if meta.AlwaysProxy {
			common = append([]FieldSpec{}, CommonRemoteFields...)
			for i := range common {
				if common[i].Name == "proxy" {
					common[i] = lockedProxyField
				}
			}
		}
		meta.Fields = append(append([]FieldSpec{}, meta.Fields...), common...)
	}
	meta.Fields = append(append([]FieldSpec{}, meta.Fields...), CommonFields...)
	registry[meta.Name] = struct {
		meta Meta
		fn   factory
	}{meta, fn}
}

func Get(name string) (factory, bool) {
	e, ok := registry[name]
	if !ok {
		return nil, false
	}
	return e.fn, true
}

func Metas() []Meta {
	out := make([]Meta, 0, len(registry))
	for _, e := range registry {
		out = append(out, e.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MetaOf 返回驱动元信息。
func MetaOf(name string) (Meta, bool) {
	e, ok := registry[name]
	return e.meta, ok
}

// AlwaysProxy 该驱动是否恒经服务器转发（未注册的驱动按 false）。
func AlwaysProxy(name string) bool {
	return registry[name].meta.AlwaysProxy
}
