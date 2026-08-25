package apptest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/config"
)

func TestPermanentRenderFailureReturnsTheSlotToItsOriginalQuotaDay(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) {
		cfg.DailyRenderCapacity = 1
		cfg.RenderMaxAttempts = 2
	})
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)
	firstDay := clock.QuotaDay(h.clock.Now())

	if submitted := h.submitRender(token, board.ShotIDs[0], ""); submitted.Status != http.StatusAccepted {
		t.Fatalf("第一天提交渲染返回 %d，应当被受理: %s", submitted.Status, submitted.Body)
	}
	renderer := NewScriptedRenderer(Fail("渲染节点显存不足"), Fail("渲染节点显存不足"))
	if processed := h.processRenders(renderer, 1); processed != 1 {
		t.Fatal("worker 没有领取第一天提交的渲染任务")
	}

	h.clock.Advance(26 * time.Hour)
	secondDay := clock.QuotaDay(h.clock.Now())
	if secondDay == firstDay {
		t.Fatalf("时间推进后额度日仍然是 %s，测试前置条件不成立", secondDay)
	}
	if _, err := h.app.Services.Render.RequeueDue(context.Background(), 10); err != nil {
		t.Fatalf("巡检重新入队失败: %v", err)
	}
	if processed := h.processRenders(renderer, 1); processed != 1 {
		t.Fatal("worker 没有在第二天领取重新入队的任务")
	}

	token = h.directorToken()
	jobs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	job := jobs.Decoded["items"].([]any)[0].(map[string]any)
	if job["state"] != "failed_permanent" {
		t.Fatalf("跨日重试后任务状态是 %v，应当已经耗尽尝试预算", job["state"])
	}
	if day, _ := job["quota_day"].(string); day != firstDay {
		t.Fatalf("任务记录的额度日是 %q，应当仍是提交那天 %q", day, firstDay)
	}

	firstDayQuota, err := h.app.Repositories.Quotas.Get(context.Background(), h.app.DB.Reader(), shotStudioID(t, h), firstDay)
	if err != nil {
		t.Fatalf("读取第一天额度账本失败: %v", err)
	}
	if firstDayQuota.Used != 0 {
		t.Fatalf("第一天额度仍占用 %d 个槽位，永久失败后应当已经归还", firstDayQuota.Used)
	}

	secondDayQuota := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if used := int(secondDayQuota.Decoded["used"].(float64)); used != 0 {
		t.Fatalf("第二天额度占用是 %d，这次补偿不应落到第二天的账本上", used)
	}
	if remaining := int(secondDayQuota.Decoded["remaining"].(float64)); remaining != 1 {
		t.Fatalf("第二天剩余额度是 %d，应当仍是当日上限 1", remaining)
	}

	if state := h.shotState(token, board, 1); state != "bound" {
		t.Fatalf("永久失败后分镜停在 %q，应当回到可重新提交的状态", state)
	}
	if resubmitted := h.submitRender(token, board.ShotIDs[0], ""); resubmitted.Status != http.StatusAccepted {
		t.Fatalf("第二天重新提交渲染返回 %d，应当占用当天唯一的额度: %s", resubmitted.Status, resubmitted.Body)
	}
	exhausted := h.submitRender(token, board.ShotIDs[1], "")
	if exhausted.Status != http.StatusTooManyRequests {
		t.Fatalf("第二天第二次提交返回 %d，应当因为超出当日上限被拒绝: %s", exhausted.Status, exhausted.Body)
	}
}
