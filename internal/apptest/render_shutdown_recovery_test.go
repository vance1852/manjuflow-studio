package apptest

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/worker"
)

// shutdownRenderer 模拟停机：先取消 worker 的工作 context，再返回一次普通渲染失败。
type shutdownRenderer struct {
	cancel func()
	calls  int
}

func (r *shutdownRenderer) Render(ctx context.Context, request worker.Request) (string, error) {
	r.calls++
	r.cancel()
	return "", apperr.New(apperr.CodeInternal, "渲染节点在停机时中断")
}

func TestInterruptedRenderStaysRecoverableAfterShutdown(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if submitted := h.submitRender(token, board.ShotIDs[0], ""); submitted.Status != http.StatusAccepted {
		t.Fatalf("渲染提交返回 %d，应当被受理: %s", submitted.Status, submitted.Body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupted := worker.NewRenderWorker(worker.RenderOptions{
		Name:     "shutdown-worker",
		Renders:  h.app.Services.Render,
		Renderer: &shutdownRenderer{cancel: cancel},
		Logger:   logging.New(io.Discard, logging.LevelError),
		Interval: time.Millisecond,
	})
	if _, err := interrupted.ProcessOne(ctx); err != nil && !apperr.IsCode(err, apperr.CodeCanceled) {
		t.Fatalf("停机中断的渲染循环返回了意外错误: %v", err)
	}

	jobs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	items := jobs.Decoded["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("该分镜有 %d 个渲染任务，应当只有 1 个", len(items))
	}
	job := items[0].(map[string]any)
	if job["state"] != "retrying" {
		t.Fatalf("停机中断后任务停在 %v，应当回到可重试状态", job["state"])
	}
	if reason, _ := job["last_error"].(string); reason == "" {
		t.Fatal("停机中断的失败原因没有落库，任务无法解释也无法追溯")
	}
	if int(job["attempts"].(float64)) != 1 {
		t.Fatalf("尝试次数是 %v，应当只记录一次中断尝试", job["attempts"])
	}

	h.clock.Advance(h.cfg.RenderBackoffBase * 4)
	requeued, err := h.app.Services.Render.RequeueDue(context.Background(), 10)
	if err != nil {
		t.Fatalf("巡检重新入队失败: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("巡检重新入队 %d 个任务，应当恰好把这一个任务放回队列", requeued)
	}
	if processed := h.processRenders(NewScriptedRenderer(Succeed("manju://after-shutdown/take")), 3); processed != 1 {
		t.Fatalf("重启后的 worker 处理了 %d 个任务，应当接手这一个任务", processed)
	}
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("恢复渲染后分镜停在 %q，应当已经产出素材", state)
	}
}
