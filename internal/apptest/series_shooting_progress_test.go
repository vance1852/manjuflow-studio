package apptest

import (
	"net/http"
	"testing"

	"github.com/vance1852/manjuflow-studio/internal/worker"
)

// TestSeriesStaysInShootingUntilEveryShotHasFootage pins the storyboard progress
// invariant: a series may only leave shooting once every one of its shots owns
// an artifact, and while it is shooting the director must still be able to
// rebind prompt versions on the shots that were not rendered yet.
func TestSeriesStaysInShootingUntilEveryShotHasFootage(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 3)

	// 只给第一个分镜排渲染，另外两个分镜只完成了提示词绑定。
	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("第一个分镜提交渲染返回 %d，应当被受理: %s", reply.Status, reply.Body)
	}
	if processed := h.processRenders(worker.StoryboardRenderer{}, 4); processed != 1 {
		t.Fatalf("渲染工作者处理了 %d 个任务，应当只处理已排队的那一个", processed)
	}
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("第一个分镜的状态是 %q，应当已经产出素材", state)
	}
	if state := h.seriesState(token, board.SeriesID); state != "shooting" {
		t.Fatalf("还有两个分镜没有产出素材时剧集状态是 %q，应当仍然停在拍摄中", state)
	}

	// 剧集仍在拍摄中，因此尚未渲染的分镜还能换绑提示词版本并继续排渲染。
	rebound := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[1]) + "/prompt",
		token:   token,
		payload: map[string]any{"prompt_version_id": board.VersionID},
	})
	if rebound.Status != http.StatusOK {
		t.Fatalf("给尚未渲染的分镜换绑提示词版本返回 %d，应当仍被接受: %s", rebound.Status, rebound.Body)
	}
	if reply := h.submitRender(token, board.ShotIDs[1], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("第二个分镜提交渲染返回 %d，应当被受理: %s", reply.Status, reply.Body)
	}
	if processed := h.processRenders(worker.StoryboardRenderer{}, 4); processed != 1 {
		t.Fatalf("第二轮渲染处理了 %d 个任务，应当只处理新排队的那一个", processed)
	}
	if state := h.seriesState(token, board.SeriesID); state != "shooting" {
		t.Fatalf("第三个分镜还没有产出素材时剧集状态是 %q，应当仍然停在拍摄中", state)
	}

	// 最后一个分镜产出素材之后，剧集才允许自动进入评审。
	if reply := h.submitRender(token, board.ShotIDs[2], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("第三个分镜提交渲染返回 %d，应当被受理: %s", reply.Status, reply.Body)
	}
	if processed := h.processRenders(worker.StoryboardRenderer{}, 4); processed != 1 {
		t.Fatalf("第三轮渲染处理了 %d 个任务，应当只处理新排队的那一个", processed)
	}
	if state := h.seriesState(token, board.SeriesID); state != "reviewing" {
		t.Fatalf("全部分镜都产出素材后剧集状态是 %q，应当进入评审中", state)
	}
}
