package server

import (
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/db"
	"newlist/internal/driver"
	"newlist/internal/fs"
	"newlist/internal/util"
)

type storageDTO struct {
	ID        int64             `json:"id"`
	MountPath string            `json:"mount_path"`
	Driver    string            `json:"driver"`
	Config    map[string]string `json:"config"`
	Ord       int               `json:"ord"`
	Enabled   bool              `json:"enabled"`
	Status    string            `json:"status"`
	CreatedAt string            `json:"created_at"`
}

func normMount(p string) string {
	return path.Clean("/" + strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"))
}

// maskSecrets 把 secret 字段值替换为 ***（回显用）。
func maskSecrets(drv string, cfg map[string]string) map[string]string {
	meta, ok := driver.MetaOf(drv)
	if !ok {
		return cfg
	}
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range meta.Fields {
		if f.Secret && out[f.Name] != "" {
			out[f.Name] = "***"
		}
	}
	return out
}

// missingRequired 返回该驱动缺填的必填字段标签。
// 表单上的红星此前只是装饰：留空照样"保存成功"，直到挂载时才失败并显示成一条红色状态。
// 调用方须在 "***" 还原成旧值之后再调，否则编辑时会把"不修改"误判为缺填。
func missingRequired(drv string, cfg map[string]string) []string {
	meta, ok := driver.MetaOf(drv)
	if !ok {
		return nil
	}
	var miss []string
	for _, f := range meta.Fields {
		if f.Required && strings.TrimSpace(cfg[f.Name]) == "" {
			miss = append(miss, f.Label)
		}
	}
	return miss
}

// GET /api/admin/drivers
func (s *Server) driverList(c *gin.Context) {
	OK(c, driver.Metas())
}

func (s *Server) loadStorages() ([]*storageDTO, error) {
	rows, err := s.db.Query(
		`SELECT id, mount_path, driver, config, ord, enabled, created_at FROM storages ORDER BY ord, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 挂载状态来自 fs 运行时（Init 失败原因等）
	status := map[int64]string{}
	for _, m := range s.fs.Mounts() {
		status[m.ID] = m.Status
	}
	var out []*storageDTO
	for rows.Next() {
		d := &storageDTO{}
		var cfgJSON string
		var enabled int
		if err := rows.Scan(&d.ID, &d.MountPath, &d.Driver, &cfgJSON, &d.Ord, &enabled, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Enabled = enabled != 0
		d.Status = status[d.ID]
		if err := json.Unmarshal([]byte(cfgJSON), &d.Config); err != nil || d.Config == nil {
			d.Config = map[string]string{}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GET /api/admin/storages
func (s *Server) storageList(c *gin.Context) {
	list, err := s.loadStorages()
	if err != nil {
		Fail500(c, err)
		return
	}
	for _, d := range list {
		d.Config = maskSecrets(d.Driver, d.Config)
	}
	OK(c, list)
}

// GET /api/admin/storages/:id —— 单条明文回显（编辑弹窗用）：secret 字段不脱敏，
// 让「显示密码」眼睛能看到原文；列表接口仍保持 ***。
func (s *Server) storageGet(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	list, err := s.loadStorages()
	if err != nil {
		Fail500(c, err)
		return
	}
	for _, d := range list {
		if d.ID == id {
			OK(c, d)
			return
		}
	}
	Fail(c, 404, "存储不存在")
}

// indexNeutral 是改了也不影响文件清单的配置字段：展示开关只决定媒体库/搜索要不要展示
// （查询时按挂载实时过滤），代理与加速只改取流方式。改这些不必动索引。
var indexNeutral = map[string]bool{
	"show_video": true, "show_photo": true, "show_search": true,
	"proxy": true, "threads": true, "chunk_mb": true,
}

// contentCfgChanged 判断两份配置里"决定这个盘有哪些文件"的字段有没有变。
func contentCfgChanged(a, b map[string]string) bool {
	for k, v := range a {
		if !indexNeutral[k] && b[k] != v {
			return true
		}
	}
	for k, v := range b {
		if !indexNeutral[k] && a[k] != v {
			return true
		}
	}
	return false
}

// mountByID 返回该存储当前的挂载（未挂上返回 nil）。
func (s *Server) mountByID(id int64) *fs.Mount {
	for _, m := range s.fs.Mounts() {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// rescanMount 让这个盘的索引跟上：挂载正常就后台只重扫它一个，返回是否真的开扫。
func (s *Server) rescanMount(id int64) bool {
	m := s.mountByID(id)
	if m == nil || !m.Enabled || m.Status != "" {
		return false
	}
	s.index.ReplaceSubtree(m.Path)
	return true
}

// afterStorageChange 增删改存储后重载挂载树，再由 apply 对索引做最小改动
// （apply 返回是否开了后台重扫；传 nil 表示这次改动与索引无关）。
//
// 过去这里无差别触发全量重建：扫遍所有网盘，完了还全库预载一遍——改个排序、关一下
// 「在视频库展示」也得等几分钟。真正需要动索引的只有被改的那一个盘，多数改动连扫都不用扫。
func (s *Server) afterStorageChange(c *gin.Context, apply func() bool) {
	if err := s.fs.Reload(c.Request.Context()); err != nil {
		Fail(c, 500, "存储已保存，但挂载重载失败："+util.Humanize(err))
		return
	}
	scanning := false
	if apply != nil {
		scanning = apply()
	}
	OK(c, gin.H{"scanning": scanning})
}

// POST /api/admin/storages
func (s *Server) storageCreate(c *gin.Context) {
	var req storageDTO
	if err := c.ShouldBindJSON(&req); err != nil || req.MountPath == "" || req.Driver == "" {
		Fail(c, 400, "挂载路径和驱动不能为空")
		return
	}
	if _, ok := driver.Get(req.Driver); !ok {
		Fail(c, 400, "不支持的驱动类型："+req.Driver)
		return
	}
	mp := normMount(req.MountPath)
	if req.Config == nil {
		req.Config = map[string]string{}
	}
	if miss := missingRequired(req.Driver, req.Config); len(miss) > 0 {
		Fail(c, 400, "请填写："+strings.Join(miss, "、"))
		return
	}
	cfgJSON, _ := json.Marshal(req.Config)
	res, err := s.db.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES(?,?,?,?,?, '', ?)`,
		mp, req.Driver, string(cfgJSON), req.Ord, util.BoolInt(req.Enabled),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if db.IsUniqueViolation(err) {
			Fail(c, 409, "该挂载路径已存在")
			return
		}
		Fail500(c, err)
		return
	}
	id, _ := res.LastInsertId()
	// 新盘的文件还不在索引里，只扫它一个（挂载失败则不扫，修好后点重载再说）
	s.afterStorageChange(c, func() bool { return s.rescanMount(id) })
}

// PUT /api/admin/storages/:id —— config 中值为 "***" 的 secret 字段保留旧值。
func (s *Server) storageUpdate(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	var req storageDTO
	if err := c.ShouldBindJSON(&req); err != nil || req.MountPath == "" || req.Driver == "" {
		Fail(c, 400, "挂载路径和驱动不能为空")
		return
	}
	if _, ok := driver.Get(req.Driver); !ok {
		Fail(c, 400, "不支持的驱动类型："+req.Driver)
		return
	}
	// 旧值决定索引要不要动、动多少（见下方 afterStorageChange 的分支）
	var oldJSON, oldPath, oldDriver string
	var oldEnabled int
	if err := s.db.QueryRow(`SELECT config, mount_path, driver, enabled FROM storages WHERE id=?`, id).
		Scan(&oldJSON, &oldPath, &oldDriver, &oldEnabled); err != nil {
		Fail(c, 404, "存储不存在")
		return
	}
	oldCfg := map[string]string{}
	json.Unmarshal([]byte(oldJSON), &oldCfg)
	if req.Config == nil {
		req.Config = map[string]string{}
	}
	for k, v := range req.Config {
		if v == "***" {
			req.Config[k] = oldCfg[k]
		}
	}
	if miss := missingRequired(req.Driver, req.Config); len(miss) > 0 {
		Fail(c, 400, "请填写："+strings.Join(miss, "、"))
		return
	}
	cfgJSON, _ := json.Marshal(req.Config)
	newPath, oldPath := normMount(req.MountPath), normMount(oldPath)
	_, err := s.db.Exec(
		`UPDATE storages SET mount_path=?, driver=?, config=?, ord=?, enabled=? WHERE id=?`,
		newPath, req.Driver, string(cfgJSON), req.Ord, util.BoolInt(req.Enabled), id)
	if err != nil {
		if db.IsUniqueViolation(err) {
			Fail(c, 409, "该挂载路径已存在")
			return
		}
		Fail500(c, err)
		return
	}
	s.afterStorageChange(c, func() bool {
		switch {
		case !req.Enabled:
			// 停用：这个盘的内容不该再出现在搜索和媒体库里
			s.index.DeletePrefix(oldPath)
			if newPath != oldPath {
				s.index.DeletePrefix(newPath)
			}
			return false
		case oldEnabled == 0 || oldDriver != req.Driver || contentCfgChanged(oldCfg, req.Config):
			// 换驱动、换账号、换根目录、由停用改启用：文件清单可能整个变了，重扫这一个盘
			if newPath != oldPath {
				s.index.DeletePrefix(oldPath)
			}
			return s.rescanMount(id)
		case newPath != oldPath:
			// 只是挪了挂载路径：内容没变，索引里整体改前缀即可，不必重扫
			s.index.DeletePrefix(newPath) // 目标路径下若有残留先清掉（path 是主键，改名会撞）
			s.index.RenamePrefix(oldPath, newPath)
			return false
		default:
			return false // 排序、展示开关、代理加速：与索引无关
		}
	})
}

// DELETE /api/admin/storages/:id
func (s *Server) storageDelete(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	// 挂载路径要在删之前取（删完就查不到了），删完把这个盘的索引行摘掉
	var mp string
	if err := s.db.QueryRow(`SELECT mount_path FROM storages WHERE id=?`, id).Scan(&mp); err != nil {
		Fail(c, 404, "存储不存在")
		return
	}
	res, err := s.db.Exec(`DELETE FROM storages WHERE id=?`, id)
	if err != nil {
		Fail500(c, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		Fail(c, 404, "存储不存在")
		return
	}
	s.afterStorageChange(c, func() bool {
		s.index.DeletePrefix(normMount(mp))
		return false
	})
}

// POST /api/admin/storages/:id/reload —— 重载全部挂载（驱动 Init 是全量重建），
// 并把这一个盘的索引重扫一遍。
// 重载的典型场景是"刚修好一个失败的挂载"：只 Reload 不扫的话挂载确实好了，索引里却仍然
// 没有它的文件——搜索和媒体库依旧空着，看起来像没修好。别的存储的索引不动。
func (s *Server) storageReload(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	s.afterStorageChange(c, func() bool { return s.rescanMount(id) })
}
