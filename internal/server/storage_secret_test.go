package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	_ "newlist/internal/driver/onedrive" // 注册带 secret 字段的 onedrive 驱动（生产由 main 引入）
)

// 存储 secret 回显：列表接口脱敏为 ***，单条接口返回原文（编辑弹窗点「眼睛」看原文用）。
func TestStorageSecretEcho(t *testing.T) {
	api, token, _, srv, _ := newOfflineTestServer(t)

	cfgJSON, _ := json.Marshal(map[string]string{
		"region": "global", "client_id": "cid-1",
		"client_secret": "raw-secret-明文", "refresh_token": "rt-原文", "root_folder_path": "/",
	})
	// 直接入库（enabled=0 不触发驱动 Init），列表/单条接口只读 DB
	res, err := srv.db.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/od', 'onedrive', ?, 1, 0, '', '2026-07-11T00:00:00Z')`, string(cfgJSON))
	if err != nil {
		t.Fatalf("插入 onedrive 存储: %v", err)
	}
	id, _ := res.LastInsertId()

	type dto struct {
		ID     int64             `json:"id"`
		Config map[string]string `json:"config"`
	}

	// 列表：secret 字段应为 ***，非 secret 字段原样
	resp, out := authedJSON(t, http.MethodGet, api+"/api/admin/storages", token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("列表应 200，得到 %d", resp.StatusCode)
	}
	var list []dto
	if err := json.Unmarshal(out["data"], &list); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	var found *dto
	for i := range list {
		if list[i].ID == id {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("列表中未找到新插入存储 id=%d", id)
	}
	if found.Config["client_secret"] != "***" || found.Config["refresh_token"] != "***" {
		t.Fatalf("列表 secret 字段应脱敏为 ***，得到 %q / %q",
			found.Config["client_secret"], found.Config["refresh_token"])
	}
	if found.Config["client_id"] != "cid-1" {
		t.Fatalf("非 secret 字段不应被脱敏，得到 %q", found.Config["client_id"])
	}

	// 单条：应返回 secret 原文
	resp, out = authedJSON(t, http.MethodGet, fmt.Sprintf("%s/api/admin/storages/%d", api, id), token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("单条应 200，得到 %d", resp.StatusCode)
	}
	var one dto
	if err := json.Unmarshal(out["data"], &one); err != nil {
		t.Fatalf("解析单条: %v", err)
	}
	if one.Config["client_secret"] != "raw-secret-明文" || one.Config["refresh_token"] != "rt-原文" {
		t.Fatalf("单条应返回 secret 原文，得到 %q / %q",
			one.Config["client_secret"], one.Config["refresh_token"])
	}

	// 不存在的 id → 404
	resp, _ = authedJSON(t, http.MethodGet, api+"/api/admin/storages/999999", token, "")
	if resp.StatusCode != 404 {
		t.Fatalf("不存在的存储应 404，得到 %d", resp.StatusCode)
	}

	// 未登录不可见明文（admin 组鉴权兜底）
	plain, err := http.Get(fmt.Sprintf("%s/api/admin/storages/%d", api, id))
	if err != nil {
		t.Fatalf("匿名请求: %v", err)
	}
	plain.Body.Close()
	if plain.StatusCode != 401 {
		t.Fatalf("匿名访问单条应 401，得到 %d", plain.StatusCode)
	}
}
