package server

import (
	"path"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/fs"
)

type mediaItem struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// baseFilter 追加用户 base_path 视野过滤条件。
func baseFilter(sql string, args []any, base string) (string, []any) {
	sql += ` AND (?='/' OR path=? OR substr(path,1,length(?)+1)=?||'/')`
	return sql, append(args, base, base, base, base)
}

// mediaVisFilter 追加"挂载可见性"过滤：文件归属挂载（最长前缀匹配）关闭了对应界面的
// 展示开关（kind=video/image/search → show_video/show_photo/show_search）时排除。
// col 为路径列（JOIN 场景传 f.path）。全部挂载都展示时不追加条件（默认情形零开销）。
func (s *Server) mediaVisFilter(sql string, args []any, kind, col string) (string, []any) {
	mounts := s.fs.Mounts() // 已按挂载路径长度降序 → CASE 命中即最长前缀
	allVisible := true
	for _, m := range mounts {
		if !m.MediaVisible(kind) {
			allVisible = false
			break
		}
	}
	if allVisible {
		return sql, args
	}
	sql += ` AND (CASE`
	for _, m := range mounts {
		sql += ` WHEN ` + col + `=? OR substr(` + col + `,1,length(?)+1)=?||'/' THEN ?`
		vis := 0
		if m.MediaVisible(kind) {
			vis = 1
		}
		args = append(args, m.Path, m.Path, m.Path, vis)
	}
	sql += ` ELSE 1 END)=1`
	return sql, args
}

// GET /api/media/list?kind=video|image&sort=modified|name&order=asc|desc&limit=&offset=&parent=
func (s *Server) mediaList(c *gin.Context) {
	kind := c.Query("kind")
	if kind != "video" && kind != "image" {
		Fail(c, 400, "kind 必须是 video 或 image")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "60"))
	if limit <= 0 || limit > 500 {
		limit = 60
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	sortCol := "modified"
	if c.Query("sort") == "name" {
		sortCol = "name"
	}
	dir := "DESC"
	if c.Query("order") == "asc" {
		dir = "ASC"
	}
	orderBy := sortCol + " " + dir
	if c.Query("sort") == "random" { // 随机抽样（Featured 推荐位）
		orderBy = "RANDOM()"
	}

	sql := `SELECT path, name, size, modified FROM files WHERE is_dir=0 AND ext_type=?`
	args := []any{kind}
	sql, args = baseFilter(sql, args, getUser(c).BasePath)
	sql, args = s.mediaVisFilter(sql, args, kind, "path")
	if parent := c.Query("parent"); parent != "" {
		p, err := fs.NormPath(parent)
		if err != nil {
			fsError(c, err)
			return
		}
		sql += ` AND substr(path,1,length(?)+1)=?||'/'`
		args = append(args, p, p)
	}
	sql += ` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		Fail500(c, err)
		return
	}
	defer rows.Close()
	items := []mediaItem{}
	for rows.Next() {
		var it mediaItem
		if err := rows.Scan(&it.Path, &it.Name, &it.Size, &it.Modified); err != nil {
			Fail500(c, err)
			return
		}
		items = append(items, it)
	}
	OK(c, gin.H{"items": items})
}

// POST /api/media/played {path, position?, duration?} —— 记录一次播放。
// 「最近播放」货架数据源，并保存断点续播位置（position/duration，秒）。
// 视频起播即上报（position=0 只刷 played_at），播放中定时上报进度；
// 图片上报无 position/duration（保持"最近查看"语义）。
func (s *Server) mediaPlayed(c *gin.Context) {
	var req struct {
		Path     string  `json:"path" binding:"required"`
		Position float64 `json:"position"`
		Duration float64 `json:"duration"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, 400, "参数错误")
		return
	}
	p, err := fs.NormPath(req.Path)
	if err != nil {
		fsError(c, err)
		return
	}
	if req.Position < 0 {
		req.Position = 0
	}
	if req.Duration < 0 {
		req.Duration = 0
	}
	// 只认索引内本用户可见的媒体文件
	sqlq := `SELECT 1 FROM files WHERE path=? AND is_dir=0 AND ext_type IN ('video','image')`
	args := []any{p}
	sqlq, args = baseFilter(sqlq, args, getUser(c).BasePath)
	var one int
	if err := s.db.QueryRow(sqlq, args...).Scan(&one); err != nil {
		Fail(c, 404, "文件不存在")
		return
	}
	// 看到接近片尾（≥95% 或剩余 ≤5s）视为看完 → position 归零，下次从头播。
	if req.Duration > 0 && (req.Position >= req.Duration-5 || req.Position/req.Duration >= 0.95) {
		req.Position = 0
	}
	// duration 仅在本次带上（>0）时更新——播放器 ready 早于元数据就绪时会上报 0，
	// 不能让它把已知时长覆盖掉（否则进度条画不出）。position 允许写 0（从头/看完归零）。
	_, err = s.db.Exec(`INSERT INTO play_history(user_id, path, played_at, position, duration) VALUES(?,?,?,?,?)
		ON CONFLICT(user_id, path) DO UPDATE SET played_at=excluded.played_at,
			position=excluded.position,
			duration=CASE WHEN excluded.duration>0 THEN excluded.duration ELSE play_history.duration END`,
		getUser(c).ID, p, time.Now().UTC().Format(time.RFC3339), req.Position, req.Duration)
	if err != nil {
		Fail500(c, err)
		return
	}
	OK(c, nil)
}

// GET /api/media/progress?path= —— 查询单个文件的续播位置（供播放页起播定位）。
// 无历史记录返回 position=0；文件不存在校验交给播放本身，此处只读历史。
func (s *Server) mediaProgress(c *gin.Context) {
	p, err := fs.NormPath(c.Query("path"))
	if err != nil {
		fsError(c, err)
		return
	}
	var pos, dur float64
	s.db.QueryRow(`SELECT position, duration FROM play_history WHERE user_id=? AND path=?`,
		getUser(c).ID, p).Scan(&pos, &dur)
	OK(c, gin.H{"position": pos, "duration": dur})
}

type historyItem struct {
	mediaItem
	PlayedAt string  `json:"played_at"`
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
}

// GET /api/media/history?kind=video|image&limit= —— 本用户最近播放，文件已删/移走的自然消失。
func (s *Server) mediaHistory(c *gin.Context) {
	kind := c.Query("kind")
	if kind != "video" && kind != "image" {
		Fail(c, 400, "kind 必须是 video 或 image")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "12"))
	if limit <= 0 || limit > 100 {
		limit = 12
	}
	base := getUser(c).BasePath
	// JOIN 后 path 有歧义，视野过滤在 f.path 上展开（同 baseFilter 语义）
	sqlq := `SELECT f.path, f.name, f.size, f.modified, h.played_at, h.position, h.duration
		FROM play_history h JOIN files f ON f.path=h.path
		WHERE h.user_id=? AND f.is_dir=0 AND f.ext_type=?
		AND (?='/' OR f.path=? OR substr(f.path,1,length(?)+1)=?||'/')`
	args := []any{getUser(c).ID, kind, base, base, base, base}
	sqlq, args = s.mediaVisFilter(sqlq, args, kind, "f.path")
	sqlq += ` ORDER BY h.played_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(sqlq, args...)
	if err != nil {
		Fail500(c, err)
		return
	}
	defer rows.Close()
	items := []historyItem{}
	for rows.Next() {
		var it historyItem
		if err := rows.Scan(&it.Path, &it.Name, &it.Size, &it.Modified, &it.PlayedAt, &it.Position, &it.Duration); err != nil {
			Fail500(c, err)
			return
		}
		items = append(items, it)
	}
	OK(c, gin.H{"items": items})
}

type mediaGroup struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Count  int64  `json:"count"`
	Cover  string `json:"cover"`
	Latest string `json:"latest"`
}

// GET /api/media/groups?kind= —— 按父目录分组（视频合集 / 相册）。
func (s *Server) mediaGroups(c *gin.Context) {
	kind := c.Query("kind")
	if kind != "video" && kind != "image" {
		Fail(c, 400, "kind 必须是 video 或 image")
		return
	}
	base := getUser(c).BasePath
	sql := `SELECT parent, COUNT(*), MAX(modified) FROM files WHERE is_dir=0 AND ext_type=?`
	args := []any{kind}
	sql, args = baseFilter(sql, args, base)
	sql, args = s.mediaVisFilter(sql, args, kind, "path")
	sql += ` GROUP BY parent ORDER BY MAX(modified) DESC LIMIT 40`

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		Fail500(c, err)
		return
	}
	groups := []*mediaGroup{}
	for rows.Next() {
		g := &mediaGroup{}
		if err := rows.Scan(&g.Dir, &g.Count, &g.Latest); err != nil {
			rows.Close()
			Fail500(c, err)
			return
		}
		g.Name = path.Base(g.Dir)
		if g.Dir == "/" {
			g.Name = "/"
		}
		groups = append(groups, g)
	}
	rows.Close()
	// 每组封面 = 组内 modified 最新的文件
	for _, g := range groups {
		s.db.QueryRow(
			`SELECT path FROM files WHERE parent=? AND is_dir=0 AND ext_type=?
			 ORDER BY modified DESC LIMIT 1`, g.Dir, kind).Scan(&g.Cover)
	}
	OK(c, gin.H{"groups": groups})
}
