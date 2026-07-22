package server

import (
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/db"
	"newlist/internal/driver"
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

// afterStorageChange 增删改存储后：重载挂载树 + 重建索引。
func (s *Server) afterStorageChange(c *gin.Context) {
	if err := s.fs.Reload(c.Request.Context()); err != nil {
		Fail(c, 500, "存储已保存，但重载失败: "+err.Error())
		return
	}
	s.index.Rebuild()
	OK(c, nil)
}

// POST /api/admin/storages
func (s *Server) storageCreate(c *gin.Context) {
	var req storageDTO
	if err := c.ShouldBindJSON(&req); err != nil || req.MountPath == "" || req.Driver == "" {
		Fail(c, 400, "mount_path/driver 不能为空")
		return
	}
	if _, ok := driver.Get(req.Driver); !ok {
		Fail(c, 400, "未知驱动: "+req.Driver)
		return
	}
	mp := normMount(req.MountPath)
	if req.Config == nil {
		req.Config = map[string]string{}
	}
	cfgJSON, _ := json.Marshal(req.Config)
	_, err := s.db.Exec(
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
	s.afterStorageChange(c)
}

// PUT /api/admin/storages/:id —— config 中值为 "***" 的 secret 字段保留旧值。
func (s *Server) storageUpdate(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	var req storageDTO
	if err := c.ShouldBindJSON(&req); err != nil || req.MountPath == "" || req.Driver == "" {
		Fail(c, 400, "mount_path/driver 不能为空")
		return
	}
	if _, ok := driver.Get(req.Driver); !ok {
		Fail(c, 400, "未知驱动: "+req.Driver)
		return
	}
	var oldJSON string
	if err := s.db.QueryRow(`SELECT config FROM storages WHERE id=?`, id).Scan(&oldJSON); err != nil {
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
	cfgJSON, _ := json.Marshal(req.Config)
	_, err := s.db.Exec(
		`UPDATE storages SET mount_path=?, driver=?, config=?, ord=?, enabled=? WHERE id=?`,
		normMount(req.MountPath), req.Driver, string(cfgJSON), req.Ord, util.BoolInt(req.Enabled), id)
	if err != nil {
		if db.IsUniqueViolation(err) {
			Fail(c, 409, "该挂载路径已存在")
			return
		}
		Fail500(c, err)
		return
	}
	s.afterStorageChange(c)
}

// DELETE /api/admin/storages/:id
func (s *Server) storageDelete(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
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
	s.afterStorageChange(c)
}

// POST /api/admin/storages/:id/reload —— 重载全部挂载（驱动 Init 是全量重建）。
func (s *Server) storageReload(c *gin.Context) {
	if err := s.fs.Reload(c.Request.Context()); err != nil {
		Fail500(c, err)
		return
	}
	OK(c, nil)
}
