package onedrive

import (
	"errors"
	"strings"
	"testing"

	"newlist/internal/driver"
)

// TestMapGraphError 覆盖 Graph 错误 → 哨兵映射：配额/权限/已存在/不存在/非法各归其类，
// 其余未归类错误包裹 ErrUpstream 并保留原始码与文案（供界面透传，不再兜底成不透明 500）。
func TestMapGraphError(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		code    string
		message string
		want    error // errors.Is 目标
	}{
		{"quota-code", 507, "quotaLimitReached", "Quota limit reached", driver.ErrQuota},
		{"quota-insufficient", 507, "insufficientStorage", "", driver.ErrQuota},
		{"quota-by-status", 507, "somethingElse", "", driver.ErrQuota},
		{"denied-code", 403, "accessDenied", "denied", driver.ErrDenied},
		{"denied-by-status", 403, "whatever", "", driver.ErrDenied},
		{"exists", 409, "nameAlreadyExists", "", driver.ErrExist},
		{"notfound-code", 400, "itemNotFound", "", driver.ErrNotFound},
		{"notfound-by-status", 404, "x", "", driver.ErrNotFound},
		{"badname", 400, "invalidRequest", "bad", driver.ErrBadName},
		{"upstream-fallthrough", 400, "malwareDetected", "blocked", driver.ErrUpstream},
		{"upstream-5xx", 503, "serviceNotAvailable", "busy", driver.ErrUpstream},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mapGraphError(c.status, c.code, c.message)
			if !errors.Is(err, c.want) {
				t.Fatalf("mapGraphError(%d,%q) = %v，期望 errors.Is %v", c.status, c.code, err, c.want)
			}
			// ErrUpstream 分支须保留原始码/文案，便于界面一眼定因
			if c.want == driver.ErrUpstream {
				if !strings.Contains(err.Error(), c.code) || !strings.Contains(err.Error(), c.message) {
					t.Fatalf("ErrUpstream 应保留原始码/文案，得到 %q", err.Error())
				}
			}
		})
	}
}
