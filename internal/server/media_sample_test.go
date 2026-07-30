// 首页取样与分页：随机抽样不再走 ORDER BY RANDOM()，分页压进子查询后仍须保持有序不重不漏。
package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mediaItems 解析列表响应的 items，保持顺序。
func mediaItems(t *testing.T, body []byte) []mediaItem {
	t.Helper()
	var r struct {
		Data struct {
			Items []mediaItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("解析响应: %v body=%s", err, body)
	}
	return r.Data.Items
}

// mediaFixture 建 n 个 mtime 各不相同的视频（序号越大越新）。
func mediaFixture(t *testing.T, n int) (dir string, newestFirst []string) {
	t.Helper()
	dir = t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("片子-%03d.mp4", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("写样例文件: %v", err)
		}
		ts := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatalf("设置 mtime: %v", err)
		}
	}
	for i := n - 1; i >= 0; i-- {
		newestFirst = append(newestFirst, fmt.Sprintf("/片库/片子-%03d.mp4", i))
	}
	return dir, newestFirst
}

// sampleScatter 连抽 20 轮，每轮都要满额、不重、全在库内；返回抽到的不同文件数。
func sampleScatter(t *testing.T, e *storeEnv, inLib map[string]bool, limit int) int {
	t.Helper()
	union := map[string]bool{}
	url := fmt.Sprintf("%s/api/media/list?kind=video&sort=random&limit=%d", e.url, limit)
	for run := 0; run < 20; run++ {
		items := mediaItems(t, mediaGet(t, url, e.token))
		if len(items) != limit {
			t.Fatalf("第 %d 次抽样得到 %d 条，应为 %d 条", run, len(items), limit)
		}
		seen := map[string]bool{}
		for _, it := range items {
			if seen[it.Path] {
				t.Fatalf("同一次抽样里出现重复项 %s", it.Path)
			}
			seen[it.Path] = true
			if !inLib[it.Path] {
				t.Fatalf("抽到了不属于本库的 %s", it.Path)
			}
			union[it.Path] = true
		}
	}
	return len(union)
}

func libSet(paths []string) map[string]bool {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestMediaRandomSample(t *testing.T) {
	const total = 60
	dir, all := mediaFixture(t, total)
	e := newStoreEnv(t, map[string]string{"/片库": dir})

	// 抽样必须散布全库。若退化成「固定取最新的那一截」，20 轮下来只会看到 5 个左右。
	if n := sampleScatter(t, e, libSet(all), 5); n < 25 {
		t.Fatalf("20 轮只抽到 %d 个不同文件（共 %d 个），取样没有散布全库", n, total)
	}

	// 候选集比上限还小：整个取回来，不重不漏
	items := mediaItems(t, mediaGet(t, e.url+"/api/media/list?kind=video&sort=random&limit=200", e.token))
	if len(items) != total {
		t.Fatalf("limit 大于候选集时应返回全部 %d 条，得到 %d 条", total, len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.Path] {
			t.Fatalf("整库取回出现重复项 %s", it.Path)
		}
		seen[it.Path] = true
	}

	// 该类型一个文件都没有时返回空，不该报错
	if got := mediaItems(t, mediaGet(t, e.url+"/api/media/list?kind=image&sort=random&limit=5", e.token)); len(got) != 0 {
		t.Fatalf("库里没有图片，随机抽样应返回空，得到 %d 条", len(got))
	}
}

// 候选集大过 randomFullFetch 时走切窗口那支。调小阈值来覆盖，不必真造两千个文件。
// 这一支在生产里恒满足 total > randomFullFetch >= limit，所以只验「满额 + 散布」。
func TestMediaRandomSampleWindowed(t *testing.T) {
	const total = 60
	dir, all := mediaFixture(t, total)
	e := newStoreEnv(t, map[string]string{"/片库": dir})
	old := randomFullFetch
	randomFullFetch = 10
	t.Cleanup(func() { randomFullFetch = old })

	if n := sampleScatter(t, e, libSet(all), 5); n < 25 {
		t.Fatalf("20 轮只抽到 %d 个不同文件（共 %d 个），切窗口取样没有散布全库", n, total)
	}
}

// 分页压进子查询之后：顺序、不重、不漏都要和「一次全取」对得上。
func TestMediaListPagination(t *testing.T) {
	const total = 60
	dir, newestFirst := mediaFixture(t, total)
	e := newStoreEnv(t, map[string]string{"/片库": dir})

	var paged []string
	for off := 0; off < total; off += 10 {
		url := fmt.Sprintf("%s/api/media/list?kind=video&sort=modified&order=desc&limit=10&offset=%d", e.url, off)
		items := mediaItems(t, mediaGet(t, url, e.token))
		if len(items) != 10 {
			t.Fatalf("offset=%d 应取到 10 条，得到 %d 条", off, len(items))
		}
		for _, it := range items {
			paged = append(paged, it.Path)
		}
	}
	if len(paged) != total {
		t.Fatalf("翻完共 %d 条，应为 %d 条", len(paged), total)
	}
	for i := range paged {
		if paged[i] != newestFirst[i] {
			t.Fatalf("第 %d 条是 %s，应为 %s（最新在前的顺序被翻页打乱了）", i, paged[i], newestFirst[i])
		}
	}

	items := mediaItems(t, mediaGet(t, e.url+"/api/media/list?kind=video&sort=name&order=asc&limit=5", e.token))
	for i, it := range items {
		want := fmt.Sprintf("/片库/片子-%03d.mp4", i)
		if it.Path != want {
			t.Fatalf("按名称升序第 %d 条是 %s，应为 %s", i, it.Path, want)
		}
	}
}
