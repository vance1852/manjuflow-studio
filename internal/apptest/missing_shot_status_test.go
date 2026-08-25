package apptest

import (
	"net/http"
	"testing"
	"time"
)

func TestMissingShotIsReportedAsNotFoundAcrossItsEntryPoints(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)
	missing := board.ShotIDs[0] + 9000

	jobs := h.call(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(missing) + "/render-jobs",
		token:  token,
	})
	if jobs.Status != http.StatusNotFound {
		t.Fatalf("查询不存在分镜的渲染记录返回 %d，应当是资源不存在: %s", jobs.Status, jobs.Body)
	}
	if jobs.errorCode() != "not_found" {
		t.Fatalf("渲染记录查询的错误码是 %q，应当是 not_found", jobs.errorCode())
	}

	bind := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(missing) + "/prompt",
		token:   token,
		payload: map[string]any{"prompt_version_id": board.VersionID},
	})
	if bind.Status != http.StatusNotFound {
		t.Fatalf("给不存在的分镜绑定提示词返回 %d，应当是资源不存在: %s", bind.Status, bind.Body)
	}
	if bind.errorCode() != "not_found" {
		t.Fatalf("绑定提示词的错误码是 %q，应当是 not_found", bind.errorCode())
	}

	now := h.clock.Now()
	workshop := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops",
		token:  token,
		payload: map[string]any{
			"shot_id":   missing,
			"title":     "用失效编号开课",
			"capacity":  2,
			"opens_at":  now.Format(time.RFC3339),
			"closes_at": now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	})
	if workshop.Status != http.StatusNotFound {
		t.Fatalf("用不存在的分镜开课返回 %d，应当是资源不存在: %s", workshop.Status, workshop.Body)
	}

	review := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(missing) + "/review",
		token:   token,
		payload: map[string]any{"approve": true},
	})
	if review.Status != http.StatusNotFound {
		t.Fatalf("审片一个不存在的分镜返回 %d，应当是资源不存在: %s", review.Status, review.Body)
	}

	existing := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	if total := int(existing.Decoded["total"].(float64)); total != 0 {
		t.Fatalf("真实分镜的渲染记录数是 %d，此时还没有提交渲染", total)
	}
	if submitted := h.submitRender(token, board.ShotIDs[0], ""); submitted.Status != http.StatusAccepted {
		t.Fatalf("真实分镜提交渲染返回 %d，应当被受理: %s", submitted.Status, submitted.Body)
	}
}
