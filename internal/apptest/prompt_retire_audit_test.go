package apptest

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/manjuflow-studio/internal/repository"
)

func TestBlockedPromptRetirementIsStillAudited(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)
	studioID := shotStudioID(t, h)

	blocked := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  token,
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("下架仍被引用的版本返回 %d，应当被拒绝: %s", blocked.Status, blocked.Body)
	}

	events, _, err := h.app.Repositories.Audits.List(context.Background(), h.app.DB.Reader(), studioID,
		repository.AuditFilter{Action: "prompt_version.retire_blocked"}, repository.Page{Limit: 10})
	if err != nil {
		t.Fatalf("读取审计流水失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("被拒绝的下架留下了 %d 条审计，应当恰好留下 1 条可复盘记录", len(events))
	}
	blockedEvent := events[0]
	if blockedEvent.Result != "rejected" {
		t.Fatalf("拒绝审计的结果是 %q，应当标记为 rejected", blockedEvent.Result)
	}
	if blockedEvent.ActorID == 0 || blockedEvent.ActorRole != "director" {
		t.Fatalf("拒绝审计没有记录操作者: actor_id=%d role=%q", blockedEvent.ActorID, blockedEvent.ActorRole)
	}
	if blockedEvent.Detail["bound_shots"] != "1" {
		t.Fatalf("拒绝审计里的引用明细是 %#v，应当报出仍有 1 个分镜绑定该版本", blockedEvent.Detail)
	}

	references := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/references",
		token:  token,
	}, http.StatusOK)
	if int(references.Decoded["bound_shots"].(float64)) != 1 {
		t.Fatalf("引用统计显示 %v 个分镜，版本状态不应被拒绝路径改写", references.Decoded["bound_shots"])
	}

	replacement := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions",
		token:  token,
		payload: map[string]any{
			"body":     PromptBody("夜市追逐的女主角", "赛博水墨改版", 3),
			"notes":    "替换绑定用",
			"activate": true,
		},
	}, http.StatusCreated)
	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/prompt",
		token:   token,
		payload: map[string]any{"prompt_version_id": replacement.int64Field("id")},
	}, http.StatusOK)

	retired := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  token,
	}, http.StatusOK)
	if retired.stringField("status") != "retired" {
		t.Fatalf("释放引用后下架结果是 %q，应当变成 retired", retired.stringField("status"))
	}

	success, _, err := h.app.Repositories.Audits.List(context.Background(), h.app.DB.Reader(), studioID,
		repository.AuditFilter{Action: "prompt_version.retired"}, repository.Page{Limit: 10})
	if err != nil {
		t.Fatalf("读取成功下架审计失败: %v", err)
	}
	if len(success) != 1 {
		t.Fatalf("成功下架留下了 %d 条审计，应当恰好 1 条", len(success))
	}
}
