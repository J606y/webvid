package db

import (
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation 报告 err 是否为 SQLite 唯一约束冲突（用于把用户名/挂载路径重名
// 从通用 500 中分出为 409）。取代对 err.Error() 文案 "UNIQUE" 的脆弱匹配：
// modernc 的结果码可能是主码（SQLITE_CONSTRAINT）或扩展码（SQLITE_CONSTRAINT_UNIQUE），
// 两者都算。
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT:
			return true
		}
	}
	return false
}
