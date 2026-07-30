package server

import (
	"math/rand/v2"
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
	// 续播位置（秒）。列表接口 LEFT JOIN 播放历史带出，供卡片画进度条；
	// 无历史/图片恒 0，omitempty 让这类响应不多出两个空字段。
	Position float64 `json:"position,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// baseFilter 追加用户 base_path 视野过滤条件。col 为路径列名（JOIN 场景传 f.path）。
func baseFilter(sql string, args []any, base, col string) (string, []any) {
	sql += ` AND (?='/' OR ` + col + `=? OR substr(` + col + `,1,length(?)+1)=?||'/')`
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

const (
	// randomSampleWindows 随机取样切几段。一段 = 索引上连续的一截；切成多段是为了让抽到的
	// 片子散布全库，而不是同一批时间里连着的几十个——那往往就是同一部剧的同一季。
	randomSampleWindows = 8
	// randomSampleRetries 窗口撞车后的补抽次数上限。别为凑满最后一条无限查下去。
	randomSampleRetries = 8
)

// randomFullFetch 候选集不超过这个数就整个取回来在内存里洗：一次索引有序扫描而已，
// 抽样绝对均匀，也不用操心窗口撞车。绝大多数库都在这一支。测试会调小它以覆盖切窗口那支。
var randomFullFetch = 2000

// mediaFilter 拼出「本用户可见的、该类型的媒体文件」条件，作用于 files 单表（列名 path）。
func (s *Server) mediaFilter(c *gin.Context, kind, parent string) (string, []any) {
	sql := ` WHERE is_dir=0 AND ext_type=?`
	args := []any{kind}
	sql, args = baseFilter(sql, args, getUser(c).VisibleBase(), "path")
	sql, args = s.mediaVisFilter(sql, args, kind, "path")
	if parent != "" {
		sql += ` AND substr(path,1,length(?)+1)=?||'/'`
		args = append(args, parent, parent)
	}
	return sql, args
}

// mediaSelect 取一页媒体。
//
// 分页与排序压进子查询、只作用于 files 单表，取回那几行之后才 LEFT JOIN 播放历史。
// 早先是先 JOIN 再 ORDER BY ... OFFSET，SQLite 得把跳过的每一行都连一次历史表：
// 60 万行的库上「查看全部」往后翻实测要 177~560ms，压进子查询后是 4.6ms。
//
// LEFT JOIN 播放历史带出续播位置：网格卡片与「最近播放」货架用同一套进度条，
// 同一个视频不该在货架上有进度、进网格就没有。无历史的行 COALESCE 成 0。
//
// 外层必须再写一次 ORDER BY：子查询的行序不保证透传到外层。这层只排 limit 行，可忽略。
func (s *Server) mediaSelect(c *gin.Context, where string, wargs []any, orderBy string, limit, offset int) ([]mediaItem, error) {
	q := `SELECT f.path, f.name, f.size, f.modified,
			COALESCE(h.position, 0), COALESCE(h.duration, 0)
		FROM (SELECT path, name, size, modified FROM files` + where +
		` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?) f
		LEFT JOIN play_history h ON h.path=f.path AND h.user_id=?
		ORDER BY f.` + orderBy
	args := append(append([]any{}, wargs...), limit, offset, getUser(c).ID)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []mediaItem{}
	for rows.Next() {
		var it mediaItem
		if err := rows.Scan(&it.Path, &it.Name, &it.Size, &it.Modified, &it.Position, &it.Duration); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// mediaRandom 随机抽一批媒体（Featured 推荐位与「随机抽样」首页）。
//
// 早先是 ORDER BY RANDOM()：SQLite 得把候选集整个读出来排一遍，代价与 LIMIT 无关——
// 60 万行的库实测 670ms，且随行数超线性恶化。现在先数一次候选集，再在
// idx_files_ext_type 上按 modified 有序切几段随机窗口取回，同一条件下实测 34ms。
func (s *Server) mediaRandom(c *gin.Context, kind, parent string, limit int) ([]mediaItem, error) {
	where, wargs := s.mediaFilter(c, kind, parent)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files`+where, wargs...).Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return []mediaItem{}, nil
	}
	// 取回的每一段仍是索引序，洗一遍才像随机
	shuffle := func(items []mediaItem) []mediaItem {
		rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
		if len(items) > limit {
			items = items[:limit]
		}
		return items
	}
	if total <= randomFullFetch {
		all, err := s.mediaSelect(c, where, wargs, "modified DESC", total, 0)
		if err != nil {
			return nil, err
		}
		return shuffle(all), nil
	}
	// 候选集够大才切窗口。此时 total > randomFullFetch >= limit >= want，偏移量恒有效。
	windows := min(limit, randomSampleWindows)
	per := (limit + windows - 1) / windows
	seen := make(map[string]bool, limit)
	out := make([]mediaItem, 0, limit)
	for attempt := 0; len(out) < limit && attempt < windows+randomSampleRetries; attempt++ {
		want := min(per, limit-len(out))
		batch, err := s.mediaSelect(c, where, wargs, "modified DESC", want, rand.IntN(total-want+1))
		if err != nil {
			return nil, err
		}
		for _, it := range batch {
			if !seen[it.Path] { // 窗口可能撞在一起
				seen[it.Path] = true
				out = append(out, it)
			}
		}
	}
	return shuffle(out), nil
}

// GET /api/media/list?kind=video|image&sort=modified|name|random&order=asc|desc&limit=&offset=&parent=
// sort=random 不认 offset：随机抽样没有「下一页」，前端首页也不翻页。
func (s *Server) mediaList(c *gin.Context) {
	kind := c.Query("kind")
	if kind != "video" && kind != "image" {
		Fail(c, 400, "媒体类型无效（应为视频或图片）")
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
	parent := ""
	if raw := c.Query("parent"); raw != "" {
		p, err := fs.NormPath(raw)
		if err != nil {
			fsError(c, err)
			return
		}
		parent = p
	}

	if c.Query("sort") == "random" {
		items, err := s.mediaRandom(c, kind, parent, limit)
		if err != nil {
			Fail500(c, err)
			return
		}
		OK(c, gin.H{"items": items})
		return
	}

	sortCol := "modified"
	if c.Query("sort") == "name" {
		sortCol = "name"
	}
	dir := "DESC"
	if c.Query("order") == "asc" {
		dir = "ASC"
	}
	where, wargs := s.mediaFilter(c, kind, parent)
	items, err := s.mediaSelect(c, where, wargs, sortCol+" "+dir, limit, offset)
	if err != nil {
		Fail500(c, err)
		return
	}
	OK(c, gin.H{"items": items})
}

// POST /api/media/played {path, position?, duration?, ended?} —— 记录一次播放。
// 「最近播放」货架数据源，并保存断点续播位置（position/duration，秒）。
// 视频起播即上报（position=0 只刷 played_at），播放中定时上报进度；
// 图片上报无 position/duration（保持"最近查看"语义）。
func (s *Server) mediaPlayed(c *gin.Context) {
	var req struct {
		Path     string  `json:"path" binding:"required"`
		Position float64 `json:"position"`
		Duration float64 `json:"duration"`
		Ended    bool    `json:"ended"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, 400, "请求参数有误")
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
	sqlq, args = baseFilter(sqlq, args, getUser(c).VisibleBase(), "path")
	var one int
	if err := s.db.QueryRow(sqlq, args...).Scan(&one); err != nil {
		Fail(c, 404, "文件不存在")
		return
	}
	// 只有播放自然结束才算看完 → position 归零，下次从头播。
	// 拖到接近片尾不算：那只是跳着瞄了一眼，续播点应当忠实保留（早先按 ≥95% 一刀切，
	// 导致拖动/暂停的每一次上报都把续播点清掉，进度条与「继续观看」随之消失）。
	if req.Ended {
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
	PlayedAt string `json:"played_at"`
}

// GET /api/media/history?kind=video|image&limit= —— 本用户最近播放，文件已删/移走的自然消失。
func (s *Server) mediaHistory(c *gin.Context) {
	kind := c.Query("kind")
	if kind != "video" && kind != "image" {
		Fail(c, 400, "媒体类型无效（应为视频或图片）")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "12"))
	if limit <= 0 || limit > 100 {
		limit = 12
	}
	base := getUser(c).VisibleBase()
	// JOIN 后 path 有歧义，视野过滤走 f.path
	sqlq := `SELECT f.path, f.name, f.size, f.modified, h.played_at, h.position, h.duration
		FROM play_history h JOIN files f ON f.path=h.path
		WHERE h.user_id=? AND f.is_dir=0 AND f.ext_type=?`
	args := []any{getUser(c).ID, kind}
	sqlq, args = baseFilter(sqlq, args, base, "f.path")
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
		if err := rows.Scan(&it.Path, &it.Name, &it.Size, &it.Modified,
			&it.PlayedAt, &it.Position, &it.Duration); err != nil {
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
		Fail(c, 400, "媒体类型无效（应为视频或图片）")
		return
	}
	base := getUser(c).VisibleBase()
	sql := `SELECT parent, COUNT(*), MAX(modified) FROM files WHERE is_dir=0 AND ext_type=?`
	args := []any{kind}
	sql, args = baseFilter(sql, args, base, "path")
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
