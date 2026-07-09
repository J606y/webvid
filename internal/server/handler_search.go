package server

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type searchItem struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	ExtType  string `json:"ext_type"`
}

// escapeLike 转义 LIKE 通配字符（配合 ESCAPE '\'）。
func escapeLike(q string) string {
	q = strings.ReplaceAll(q, `\`, `\\`)
	q = strings.ReplaceAll(q, `%`, `\%`)
	q = strings.ReplaceAll(q, `_`, `\_`)
	return q
}

// GET /api/fs/search?q=&type=&limit=
func (s *Server) fsSearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		Fail(c, 400, "q 不能为空")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	kind := c.Query("type") // "" | video | image | other

	sql := `SELECT path, name, is_dir, size, modified, ext_type FROM files
		WHERE name_lower LIKE '%'||?||'%' ESCAPE '\'`
	args := []any{escapeLike(strings.ToLower(q))}
	if kind != "" {
		sql += ` AND ext_type=? AND is_dir=0`
		args = append(args, kind)
	}
	// base_path 视野过滤
	base := getUser(c).BasePath
	sql += ` AND (?='/' OR path=? OR substr(path,1,length(?)+1)=?||'/')`
	args = append(args, base, base, base, base)
	// 挂载「在搜索中展示」开关过滤
	sql, args = s.mediaVisFilter(sql, args, "search", "path")
	sql += ` ORDER BY is_dir DESC, name LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		Fail500(c, err)
		return
	}
	defer rows.Close()
	items := []searchItem{}
	for rows.Next() {
		var it searchItem
		var isDir int
		if err := rows.Scan(&it.Path, &it.Name, &isDir, &it.Size, &it.Modified, &it.ExtType); err != nil {
			Fail500(c, err)
			return
		}
		it.IsDir = isDir != 0
		items = append(items, it)
	}
	OK(c, gin.H{"items": items})
}
