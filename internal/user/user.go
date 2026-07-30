package user

import (
	"database/sql"
	"errors"
	"path"
	"strings"
	"sync"
	"time"

	"newlist/internal/db"
	"newlist/internal/util"
)

var (
	ErrNotFound  = errors.New("user not found")
	ErrLastAdmin = errors.New("不能删除或停用最后一个管理员")
	ErrExists    = errors.New("用户名已存在")
)

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"` // admin | user
	BasePath  string `json:"base_path"`
	CanWrite  bool   `json:"can_write"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`

	PasswordHash string `json:"-"`
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

// AllowWrite 是否允许写操作（管理员恒可写）。
func (u *User) AllowWrite() bool { return u.IsAdmin() || u.CanWrite }

// VisibleBase 生效的可见根路径。管理员恒为 "/"：base_path 在后台对管理员本就可自行编辑，
// 拿它当限制形同虚设，却会让某个管理员莫名其妙看不见半个库——与"管理员恒可写"同一条语义。
func (u *User) VisibleBase() string {
	if u.IsAdmin() || u.BasePath == "" {
		return "/"
	}
	return u.BasePath
}

// NormBasePath 归一化 base_path（POSIX 逻辑路径）。
func NormBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Clean("/" + p)
}

// idCacheTTL GetByID 的缓存有效期。每个 HTTP 请求都要过一次鉴权中间件、每次鉴权都
// 查一遍 users：媒体库首页一屏两百张封面就是两百多次 SELECT 挤在 4 条 SQLite 连接上，
// HLS 播放期间每 4 秒一个分片请求也各查一次。
//
// 缓存只在本进程读到旧数据，写操作（改资料/改密/停用/删号）会当场清掉整表缓存，
// 所以后台改完立即生效。真正会迟的只有绕过本进程直接改数据库的情形。
const idCacheTTL = 30 * time.Second

type Store struct {
	db *sql.DB

	mu     sync.Mutex
	cache  map[int64]User // 按值存：交出去的是副本，调用方改不到缓存里的对象
	cachAt map[int64]time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, cache: map[int64]User{}, cachAt: map[int64]time.Time{}}
}

// invalidate 清空 GetByID 缓存。任何写操作之后都要调——用户数量小，整表清最省心，
// 也不会漏掉「改了 A 却只清了 B」这类错。
func (s *Store) invalidate() {
	s.mu.Lock()
	s.cache = map[int64]User{}
	s.cachAt = map[int64]time.Time{}
	s.mu.Unlock()
}

const cols = `id, username, password_hash, role, base_path, can_write, enabled, created_at`

func scan(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var canWrite, enabled int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.BasePath, &canWrite, &enabled, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CanWrite = canWrite != 0
	u.Enabled = enabled != 0
	return u, nil
}

func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) Create(username, passwordHash, role, basePath string, canWrite bool) (*User, error) {
	if role != "admin" {
		role = "user"
	}
	res, err := s.db.Exec(
		`INSERT INTO users(username, password_hash, role, base_path, can_write, enabled, created_at)
		 VALUES(?,?,?,?,?,1,?)`,
		username, passwordHash, role, NormBasePath(basePath), util.BoolInt(canWrite),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if db.IsUniqueViolation(err) {
			return nil, ErrExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.invalidate()
	return s.GetByID(id)
}

// GetByID 按 ID 取用户，带 idCacheTTL 的内存缓存。返回的始终是一份独立副本。
func (s *Store) GetByID(id int64) (*User, error) {
	if u, ok := s.cached(id); ok {
		return u, nil
	}
	u, err := scan(s.db.QueryRow(`SELECT ` + cols + ` FROM users WHERE id=?`, id))
	if err != nil {
		return nil, err // 不缓存失败：删号后重建同 ID 的场景不该被负缓存挡住
	}
	s.mu.Lock()
	s.cache[id] = *u
	s.cachAt[id] = time.Now()
	s.mu.Unlock()
	return u, nil
}

func (s *Store) cached(id int64) (*User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.cachAt[id]
	if !ok || time.Since(at) > idCacheTTL {
		if ok { // 过期即删，别让停用过的账号一直占着位置
			delete(s.cache, id)
			delete(s.cachAt, id)
		}
		return nil, false
	}
	u := s.cache[id]
	return &u, true
}

func (s *Store) GetByUsername(name string) (*User, error) {
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM users WHERE username=?`, name))
}

func (s *Store) List() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + cols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Update 更新用户资料；调用方保证字段已校验。降级/停用前检查最后管理员。
func (s *Store) Update(u *User) error {
	cur, err := s.GetByID(u.ID)
	if err != nil {
		return err
	}
	if cur.IsAdmin() && cur.Enabled && (!u.IsAdmin() || !u.Enabled) {
		n, err := s.countEnabledAdmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	_, err = s.db.Exec(
		`UPDATE users SET username=?, role=?, base_path=?, can_write=?, enabled=? WHERE id=?`,
		u.Username, u.Role, NormBasePath(u.BasePath), util.BoolInt(u.CanWrite), util.BoolInt(u.Enabled), u.ID)
	s.invalidate() // 出错也清：部分语句可能已生效，宁可多查一次
	if err != nil && db.IsUniqueViolation(err) {
		return ErrExists
	}
	return err
}

func (s *Store) UpdatePassword(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, id)
	s.invalidate()
	return err
}

func (s *Store) Delete(id int64) error {
	cur, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if cur.IsAdmin() && cur.Enabled {
		n, err := s.countEnabledAdmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	_, err = s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	s.invalidate()
	return err
}

func (s *Store) countEnabledAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND enabled=1`).Scan(&n)
	return n, err
}
