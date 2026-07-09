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
}

// Meta 描述一个已注册驱动。
type Meta struct {
	Name   string      `json:"name"`
	Label  string      `json:"label"`
	Remote bool        `json:"remote"` // 远端存储：追加代理/加速等通用字段
	Fields []FieldSpec `json:"fields"`
}

type factory func() Driver

var registry = map[string]struct {
	meta Meta
	fn   factory
}{}

// CommonRemoteFields 远端驱动统一追加的字段（fs 层读取，驱动可忽略）。
var CommonRemoteFields = []FieldSpec{
	{Name: "proxy", Label: "代理模式（服务器中转流量）", Type: "bool", Default: "false",
		Help: "开启后下载/播放经服务器转发，可配合多线程加速"},
	{Name: "threads", Label: "加速线程数", Type: "number", Default: "4",
		Help: "代理模式下并发 Range 连接数，1=不加速"},
	{Name: "chunk_mb", Label: "加速分块大小(MB)", Type: "number", Default: "4"},
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

// Register 注册驱动；在驱动包的 init() 中调用。
func Register(meta Meta, fn factory) {
	if meta.Remote {
		meta.Fields = append(append([]FieldSpec{}, meta.Fields...), CommonRemoteFields...)
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
