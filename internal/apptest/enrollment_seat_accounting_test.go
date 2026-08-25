package apptest

import (
	"net/http"
	"testing"
	"time"
)

func TestDuplicateEnrolmentAttemptDoesNotConsumeASeat(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	board := publishedBoard(t, h, director, 1)

	workshop := h.openWorkshop(director, board.ShotIDs[0], 2, 72*time.Hour)
	if workshop.Status != http.StatusCreated {
		t.Fatalf("开设教学工坊返回 %d: %s", workshop.Status, workshop.Body)
	}
	workshopID := workshop.int64Field("id")

	const secondEmail = "second-learner@manjuflow.test"
	const secondPassword = "second-learner-2026"
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  director,
		payload: map[string]string{
			"email":        secondEmail,
			"display_name": "第二位学员",
			"password":     secondPassword,
			"role":         "apprentice",
		},
	}, http.StatusCreated)

	firstLearner := h.apprenticeToken()
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  firstLearner,
	}, http.StatusCreated)

	duplicate := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  firstLearner,
	})
	if duplicate.Status != http.StatusConflict {
		t.Fatalf("同一学员重复报名返回 %d，应当是冲突: %s", duplicate.Status, duplicate.Body)
	}

	secondLearner := h.login(secondEmail, secondPassword)
	joined := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  secondLearner,
	})
	if joined.Status != http.StatusCreated {
		t.Fatalf("第二位学员报名返回 %d，工坊仍有空位时应当成功: %s", joined.Status, joined.Body)
	}

	listed := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/workshops?state=open,teaching&limit=10",
		token:  director,
	}, http.StatusOK)
	items := listed.Decoded["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("工坊列表返回 %d 条，应当只有这一个工坊", len(items))
	}
	entry := items[0].(map[string]any)
	if enrolled := int(entry["enrolled"].(float64)); enrolled != 2 {
		t.Fatalf("已报名人数是 %d，应当与两位真实学员一致", enrolled)
	}

	practice := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   secondLearner,
		payload: map[string]string{"body": "第二位学员的练习：按工坊指定的提示词版本重排了分镜节奏。"},
	})
	if practice.Status != http.StatusCreated {
		t.Fatalf("第二位学员提交练习返回 %d，报名成功后应当可以提交: %s", practice.Status, practice.Body)
	}
}
