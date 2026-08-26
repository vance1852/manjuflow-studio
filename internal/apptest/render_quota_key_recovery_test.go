package apptest

import (
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/config"
)

func TestRefusedRenderKeyCanBeRetriedAfterQuotaResets(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) { cfg.DailyRenderCapacity = 1 })
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	accepted := h.submitRender(token, board.ShotIDs[0], "night-market-take-a")
	if accepted.Status != http.StatusAccepted {
		t.Fatalf("第一个分镜的渲染提交返回 %d，应当被受理: %s", accepted.Status, accepted.Body)
	}

	refused := h.submitRender(token, board.ShotIDs[1], "night-market-take-b")
	if refused.Status != http.StatusTooManyRequests {
		t.Fatalf("当日额度用尽时第二个分镜返回 %d，应当被拒绝: %s", refused.Status, refused.Body)
	}
	if refused.errorCode() != "resource_exhausted" {
		t.Fatalf("拒绝原因是 %q，应当是当日额度用尽", refused.errorCode())
	}
	sameDay := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if used := int(sameDay.Decoded["used"].(float64)); used != 1 {
		t.Fatalf("被拒绝的提交把当日额度占用改成 %d，应当仍然只有 1 个槽位被占用", used)
	}

	h.clock.Advance(26 * time.Hour)
	token = h.directorToken()

	retried := h.submitRender(token, board.ShotIDs[1], "night-market-take-b")
	if retried.Status != http.StatusAccepted {
		t.Fatalf("新额度日用同一个幂等键重试返回 %d，应当被受理: %s", retried.Status, retried.Body)
	}
	if retried.Header.Get("Idempotent-Replay") == "true" {
		t.Fatal("上一次被拒绝的提交被当成已完成结果回放，重试没有真正排队渲染")
	}
	jobID := retried.int64Field("job_id")
	if jobID == 0 {
		t.Fatalf("重试没有创建渲染任务: %s", retried.Body)
	}
	if state := h.shotState(token, board, 2); state != "rendering" {
		t.Fatalf("重试之后第二个分镜停在 %q，应当进入 rendering", state)
	}
	newDay := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if used := int(newDay.Decoded["used"].(float64)); used != 1 {
		t.Fatalf("新额度日的占用是 %d，重试应当占用一个槽位", used)
	}

	replay := h.submitRender(token, board.ShotIDs[1], "night-market-take-b")
	if replay.Status != http.StatusAccepted || replay.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("真正受理过的提交没有按同一个键回放: status=%d replay=%q", replay.Status, replay.Header.Get("Idempotent-Replay"))
	}
	if replayed := replay.int64Field("job_id"); replayed != jobID {
		t.Fatalf("回放返回任务 %d，应当仍然是 %d", replayed, jobID)
	}
}
