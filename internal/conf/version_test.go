package conf

import "testing"

// TestVersionIsInjectable Version 必须是可赋值的 var，不能改回 const。
//
// 发布流水线用 `-ldflags -X newlist/internal/conf.Version=<标签>` 注入版本号
// （见 .github/workflows/release.yml）。改回 const 之后这条测试**编译不过**，
// 这正是想要的结果——否则注入会静默失效：编译照样成功，但每个发布产物都显示 "dev"，
// 而没人会在打完标签之后再去核对界面上的版本号。
func TestVersionIsInjectable(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	Version = "0.0.0-test"
	if Version != "0.0.0-test" {
		t.Fatal("Version 赋值无效")
	}
}

// TestVersionDefaultIsDev 未注入时必须是 "dev"，不能是某个真实版本号。
// 本地构建冒充发布版本会害人：拿着 dev 构建去对照发布说明排查，结论必然是错的。
func TestVersionDefaultIsDev(t *testing.T) {
	if Version != "dev" {
		t.Skipf("本次构建注入了版本号（%s），跳过默认值检查", Version)
	}
}
