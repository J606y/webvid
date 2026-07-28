// 存储改动只对索引做最小改动：不再无差别全量重建。
// 判据是一行合成的"哨兵"索引行——它不属于任何挂载，只有全量重建的整表替换会抹掉它。
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/db"
	_ "newlist/internal/driver/local"
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

const marker = "/zzz-哨兵.txt"

type storeEnv struct {
	d     *sql.DB
	fs    *fs.FS
	idx   *index.Builder
	url   string
	token string
}

func newStoreEnv(t *testing.T, mounts map[string]string) *storeEnv {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	cf, err := conf.New(d)
	if err != nil {
		t.Fatalf("conf.New: %v", err)
	}
	secret, err := cf.JWTSecret()
	if err != nil {
		t.Fatalf("JWTSecret: %v", err)
	}
	users := user.NewStore(d)
	hash, _ := auth.HashPassword("pw123456")
	if _, err := users.Create("admin", hash, "admin", "/", true); err != nil {
		t.Fatalf("创建管理员: %v", err)
	}
	i := 0
	for mp, root := range mounts {
		cfg, _ := json.Marshal(map[string]string{"root_path": root})
		if _, err := d.Exec(
			`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
			 VALUES(?, 'local', ?, ?, 1, '', '2026-07-28T00:00:00Z')`, mp, string(cfg), i); err != nil {
			t.Fatalf("插入存储 %s: %v", mp, err)
		}
		i++
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	t.Cleanup(func() {
		// local 驱动持有根目录句柄（os.OpenRoot），Windows 下不 Drop 则 TempDir 删不掉
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
	})
	idx := index.New(d, f)
	if !idx.Rebuild() {
		t.Fatal("触发索引重建失败")
	}
	deadline := time.Now().Add(10 * time.Second)
	for idx.Progress().Running {
		if time.Now().After(deadline) {
			t.Fatal("索引重建超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e := idx.Progress().Err; e != "" {
		t.Fatalf("索引重建出错: %s", e)
	}
	srv := New(d, cf, users, f, thumb.New(f, t.TempDir()),
		media.New(f, t.TempDir(), "http://127.0.0.1:0", secret, nil), idx, nil, task.New(1), secret)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"pw123456"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	var lr struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil || lr.Data.Token == "" {
		t.Fatalf("登录响应异常: err=%v", err)
	}
	env := &storeEnv{d: d, fs: f, idx: idx, url: ts.URL, token: lr.Data.Token}
	env.putMarker(t)
	return env
}

// putMarker 写入哨兵行：任何全量重建都会把它抹掉，最小改动则不会。
func (e *storeEnv) putMarker(t *testing.T) {
	t.Helper()
	if _, err := e.d.Exec(
		`INSERT OR REPLACE INTO files(path,parent,name,name_lower,is_dir,size,modified,ext_type)
		 VALUES(?, '/', 'zzz-哨兵.txt', 'zzz-哨兵.txt', 0, 0, '', 'other')`, marker); err != nil {
		t.Fatalf("写哨兵行: %v", err)
	}
}

func (e *storeEnv) indexed(t *testing.T, p string) bool {
	t.Helper()
	var n int
	if err := e.d.QueryRow(`SELECT COUNT(*) FROM files WHERE path=?`, p).Scan(&n); err != nil {
		t.Fatalf("查询 files: %v", err)
	}
	return n > 0
}

// waitIndexed 等某条路径进/出索引（后台重扫是异步的）。
func (e *storeEnv) waitIndexed(t *testing.T, p string, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for e.indexed(t, p) != want {
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s %s索引 超时", p, map[bool]string{true: "进入", false: "离开"}[want])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// req 发一个管理接口请求，返回 data 字段。
func (e *storeEnv) req(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, e.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var r struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("%s %s 响应解析失败: %v", method, path, err)
	}
	if resp.StatusCode != 200 || r.Code != 200 {
		t.Fatalf("%s %s: status=%d code=%d msg=%s", method, path, resp.StatusCode, r.Code, r.Message)
	}
	return r.Data
}

func (e *storeEnv) storageID(t *testing.T, mountPath string) int64 {
	t.Helper()
	var id int64
	if err := e.d.QueryRow(`SELECT id FROM storages WHERE mount_path=?`, mountPath).Scan(&id); err != nil {
		t.Fatalf("查存储 %s: %v", mountPath, err)
	}
	return id
}

func put(t *testing.T, e *storeEnv, id int64, body map[string]any) bool {
	t.Helper()
	d := e.req(t, http.MethodPut, "/api/admin/storages/"+itoa(id), body)
	scanning, _ := d["scanning"].(bool)
	return scanning
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// TestStorageSaveKeepsIndex：改排序/展示开关不动索引，改挂载路径只改前缀，
// 换根目录才重扫且只扫这一个盘——全程不碰别的存储，也不做全量重建。
func TestStorageSaveKeepsIndex(t *testing.T) {
	dirA, dirB, dirC := t.TempDir(), t.TempDir(), t.TempDir()
	writeFiles(t, dirA, "a.mp4")
	writeFiles(t, dirB, "b.mp4")
	writeFiles(t, dirC, "c.mp4")
	e := newStoreEnv(t, map[string]string{"/A": dirA, "/B": dirB})
	id := e.storageID(t, "/A")

	base := func(mp, root string, extra map[string]string) map[string]any {
		cfg := map[string]string{"root_path": root}
		for k, v := range extra {
			cfg[k] = v
		}
		return map[string]any{"mount_path": mp, "driver": "local", "config": cfg, "ord": 0, "enabled": true}
	}
	assertIntact := func(step string) {
		if !e.indexed(t, marker) {
			t.Fatalf("%s：不该触发全量重建（哨兵行被抹掉）", step)
		}
		if !e.indexed(t, "/B/b.mp4") {
			t.Fatalf("%s：不该动别的存储的索引", step)
		}
		if e.idx.Progress().Running {
			t.Fatalf("%s：不该触发全量重建（重建进行中）", step)
		}
	}

	// 只改排序：与索引无关
	if put(t, e, id, map[string]any{"mount_path": "/A", "driver": "local",
		"config": map[string]string{"root_path": dirA}, "ord": 5, "enabled": true}) {
		t.Fatal("只改排序不该重扫")
	}
	assertIntact("改排序")
	if !e.indexed(t, "/A/a.mp4") {
		t.Fatal("改排序不该动这个盘的索引")
	}

	// 只关展示开关：查询时按挂载实时过滤，索引照旧
	if put(t, e, id, base("/A", dirA, map[string]string{"show_video": "false"})) {
		t.Fatal("只改展示开关不该重扫")
	}
	assertIntact("关展示开关")
	if !e.indexed(t, "/A/a.mp4") {
		t.Fatal("改展示开关不该动这个盘的索引")
	}

	// 挪挂载路径：内容没变，整体改前缀，不重扫
	if put(t, e, id, base("/A2", dirA, nil)) {
		t.Fatal("只挪挂载路径不该重扫")
	}
	assertIntact("挪挂载路径")
	if e.indexed(t, "/A/a.mp4") || !e.indexed(t, "/A2/a.mp4") {
		t.Fatal("挪挂载路径应在索引里整体改前缀")
	}

	// 换根目录：文件清单变了，只重扫这一个盘
	if !put(t, e, id, base("/A2", dirC, nil)) {
		t.Fatal("换根目录应重扫这个盘")
	}
	e.waitIndexed(t, "/A2/c.mp4", true)
	e.waitIndexed(t, "/A2/a.mp4", false)
	assertIntact("换根目录")

	// 停用：这个盘的内容从索引里摘掉，别的盘不受影响
	body := base("/A2", dirC, nil)
	body["enabled"] = false
	if put(t, e, id, body) {
		t.Fatal("停用不该重扫")
	}
	if e.indexed(t, "/A2/c.mp4") {
		t.Fatal("停用后这个盘不该留在索引里")
	}
	assertIntact("停用")
}

// TestStorageDeleteDropsOnlyItsRows：删存储只摘掉它自己的索引行。
func TestStorageDeleteDropsOnlyItsRows(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFiles(t, dirA, "a.mp4")
	writeFiles(t, dirB, "b.mp4")
	e := newStoreEnv(t, map[string]string{"/A": dirA, "/B": dirB})

	e.req(t, http.MethodDelete, "/api/admin/storages/"+itoa(e.storageID(t, "/A")), nil)
	if e.indexed(t, "/A/a.mp4") || e.indexed(t, "/A") {
		t.Fatal("删存储后它的索引行应一并摘掉")
	}
	if !e.indexed(t, "/B/b.mp4") {
		t.Fatal("删存储不该动别的存储的索引")
	}
	if !e.indexed(t, marker) {
		t.Fatal("删存储不该触发全量重建")
	}
}

// TestStorageCreateScansOnlyNewMount：新增存储只扫新盘，别的盘的索引不动。
func TestStorageCreateScansOnlyNewMount(t *testing.T) {
	dirB, dirNew := t.TempDir(), t.TempDir()
	writeFiles(t, dirB, "b.mp4")
	writeFiles(t, dirNew, "n.mp4")
	if err := os.Mkdir(filepath.Join(dirNew, "sub"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	writeFiles(t, filepath.Join(dirNew, "sub"), "deep.mp4")
	e := newStoreEnv(t, map[string]string{"/B": dirB})

	d := e.req(t, http.MethodPost, "/api/admin/storages", map[string]any{
		"mount_path": "/新盘", "driver": "local",
		"config": map[string]string{"root_path": dirNew}, "ord": 0, "enabled": true})
	if scanning, _ := d["scanning"].(bool); !scanning {
		t.Fatal("新增存储应后台扫这个新盘")
	}
	e.waitIndexed(t, "/新盘/sub/deep.mp4", true)
	if !e.indexed(t, "/新盘") || !e.indexed(t, "/新盘/n.mp4") {
		t.Fatal("新盘的挂载点与文件都应进索引")
	}
	if !e.indexed(t, "/B/b.mp4") || !e.indexed(t, marker) {
		t.Fatal("新增存储不该重建别的存储的索引")
	}
	// 「共 N 项」要跟得上：增量改动后读进度会重数一遍真实行数
	var rows int64
	if err := e.d.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&rows); err != nil {
		t.Fatalf("查询 files: %v", err)
	}
	if got := e.idx.Progress().Scanned; got != rows {
		t.Fatalf("索引条数应跟上实际行数 %d, got %d", rows, got)
	}
}
