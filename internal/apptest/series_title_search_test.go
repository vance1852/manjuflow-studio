package apptest

import (
	"net/http"
	"testing"
	"time"
)

func TestSeriesTitleSearchReportsAConsistentTotal(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	titles := []string{
		"夜市追逐上篇",
		"夜市追逐下篇",
		"雨巷独白",
		"天台告白",
		"末班车回响",
	}
	for _, title := range titles {
		h.mustCall(requestSpec{
			method:  http.MethodPost,
			path:    "/v1/series",
			token:   token,
			payload: map[string]string{"title": title, "code_prefix": "MJ"},
		}, http.StatusCreated)
		h.clock.Advance(time.Minute)
	}

	matched := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?title=%E5%A4%9C%E5%B8%82%E8%BF%BD%E9%80%90&limit=10&sort_by=code&order=asc",
		token:  token,
	}, http.StatusOK)
	items := matched.Decoded["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("标题搜索返回 %d 条剧集，应当只有两部匹配", len(items))
	}
	total := int(matched.Decoded["total"].(float64))
	if total != len(items) {
		t.Fatalf("标题搜索的总数是 %d，与实际匹配的 %d 条不一致", total, len(items))
	}

	secondPage := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?title=%E5%A4%9C%E5%B8%82%E8%BF%BD%E9%80%90&limit=2&offset=2&sort_by=code&order=asc",
		token:  token,
	}, http.StatusOK)
	if rest := secondPage.Decoded["items"].([]any); len(rest) != 0 {
		t.Fatalf("第二页返回 %d 条剧集，匹配结果只有一页", len(rest))
	}
	if restTotal := int(secondPage.Decoded["total"].(float64)); restTotal != 2 {
		t.Fatalf("第二页报出的总数是 %d，应当仍然是匹配的 2 部", restTotal)
	}

	unfiltered := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?limit=10",
		token:  token,
	}, http.StatusOK)
	if all := int(unfiltered.Decoded["total"].(float64)); all != len(titles) {
		t.Fatalf("不带过滤的总数是 %d，应当是全部 %d 部剧集", all, len(titles))
	}

	drafts := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?state=draft&limit=10",
		token:  token,
	}, http.StatusOK)
	if draftTotal := int(drafts.Decoded["total"].(float64)); draftTotal != len(titles) {
		t.Fatalf("按草稿状态过滤的总数是 %d，应当是 %d 部", draftTotal, len(titles))
	}

	none := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?title=%E4%B8%8D%E5%AD%98%E5%9C%A8%E7%9A%84%E5%89%A7%E9%9B%86&limit=10",
		token:  token,
	}, http.StatusOK)
	if noneTotal := int(none.Decoded["total"].(float64)); noneTotal != 0 {
		t.Fatalf("搜索不存在的标题时总数是 %d，应当为 0", noneTotal)
	}
}
