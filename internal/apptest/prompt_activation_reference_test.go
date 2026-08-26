package apptest

import (
	"net/http"
	"testing"
)

func TestActivatingANewPromptVersionKeepsTheReferencedOneUsable(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if submitted := h.submitRender(token, board.ShotIDs[0], ""); submitted.Status != http.StatusAccepted {
		t.Fatalf("提交渲染返回 %d，应当被受理: %s", submitted.Status, submitted.Body)
	}

	replacement := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions",
		token:  token,
		payload: map[string]any{
			"body":     PromptBody("夜市追逐的女主角", "赛博水墨第二稿", 5),
			"notes":    "第二稿",
			"activate": false,
		},
	}, http.StatusCreated)
	activated := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(replacement.int64Field("id")) + "/activate",
		token:  token,
	}, http.StatusOK)
	if activated.stringField("status") != "active" {
		t.Fatalf("新版本激活后状态是 %q，应当是 active", activated.stringField("status"))
	}

	versions := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions?limit=20&sort_by=version",
		token:  token,
	}, http.StatusOK)
	items := versions.Decoded["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("模板下有 %d 个版本，应当是两个", len(items))
	}
	for _, item := range items {
		entry := item.(map[string]any)
		if int64(entry["id"].(float64)) != board.VersionID {
			continue
		}
		if entry["status"] != "active" {
			t.Fatalf("仍被分镜绑定的旧版本变成 %v，激活新版本不应改动它", entry["status"])
		}
		if retired, _ := entry["retired_at"].(string); retired != "" {
			t.Fatalf("旧版本带上了下架时间 %q，它仍在被使用", retired)
		}
	}

	references := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/references",
		token:  token,
	}, http.StatusOK)
	if bound := int(references.Decoded["bound_shots"].(float64)); bound != 1 {
		t.Fatalf("旧版本的分镜引用数是 %d，应当仍有 1 个分镜在用", bound)
	}
	if jobs := int(references.Decoded["unfinished_jobs"].(float64)); jobs != 1 {
		t.Fatalf("旧版本的在途任务数是 %d，应当仍有 1 个渲染任务", jobs)
	}

	blocked := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  token,
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("单独下架仍被引用的旧版本返回 %d，应当被拦住: %s", blocked.Status, blocked.Body)
	}

	if processed := h.processRenders(NewScriptedRenderer(Succeed("manju://night-market/take-1")), 3); processed != 1 {
		t.Fatalf("worker 处理了 %d 个任务，使用旧版本的在途渲染应当能完成", processed)
	}
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("渲染完成后分镜停在 %q，应当已经产出素材", state)
	}
}
