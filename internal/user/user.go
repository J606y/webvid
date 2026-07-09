package user

import (
	"database/sql"
	"errors"
	"path"
	"strings"
	"time"
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

// NormBasePath 归一化 base_path（POSIX 逻辑路径）。
func NormBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Clean("/" + p)
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

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
		username, passwordHash, role, NormBasePath(basePath), boolInt(canWrite),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetByID(id)
}

func (s *Store) GetByID(id int64) (*User, error) {
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM users WHERE id=?`, id))
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
		u.Username, u.Role, NormBasePath(u.BasePath), boolInt(u.CanWrite), boolInt(u.Enabled), u.ID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	return err
}

func (s *Store) UpdatePassword(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, id)
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
	return err
}

func (s *Store) countEnabledAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND enabled=1`).Scan(&n)
	return n, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
