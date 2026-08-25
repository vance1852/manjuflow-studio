package apptest

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// publishedBoard drives the production pipeline until the series is published.
func publishedBoard(t *testing.T, h *harness, token string, shots int) storyboard {
	t.Helper()
	board := h.seedStoryboard(token, shots)
	for index := range board.ShotIDs {
		renderAndApprove(t, h, token, board, index, true)
	}
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  token,
	}, http.StatusOK)
	return board
}

func (h *harness) openWorkshop(token string, shotID int64, capacity int, window time.Duration) response {
	h.t.Helper()
	now := h.clock.Now()
	return h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops",
		token:  token,
		payload: map[string]any{
			"shot_id":   shotID,
			"title":     "夜市追逐分镜带教",
			"brief":     "跟着同一版提示词复刻镜头节奏",
			"capacity":  capacity,
			"opens_at":  now.Format(time.RFC3339),
			"closes_at": now.Add(window).Format(time.RFC3339),
		},
	})
}

func TestWorkshopRequiresAPublishedSeriesAndAnApprovedShot(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	tooEarly := h.openWorkshop(token, board.ShotIDs[0], 3, 48*time.Hour)
	if tooEarly.Status != http.StatusUnprocessableEntity {
		t.Fatalf("workshop opened on an unpublished series, status %d: %s", tooEarly.Status, tooEarly.Body)
	}

	renderAndApprove(t, h, token, board, 0, true)
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  token,
	}, http.StatusOK)

	opened := h.openWorkshop(token, board.ShotIDs[0], 3, 48*time.Hour)
	if opened.Status != http.StatusCreated {
		t.Fatalf("workshop was refused on a published series, status %d: %s", opened.Status, opened.Body)
	}
	if opened.int64Field("prompt_version_id") != board.VersionID {
		t.Fatalf("workshop froze prompt version %d, want %d", opened.int64Field("prompt_version_id"), board.VersionID)
	}
	if opened.stringField("state") != "open" {
		t.Fatalf("workshop state is %q, want open", opened.stringField("state"))
	}
}

func TestWorkshopScheduleIsValidated(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := publishedBoard(t, h, token, 1)

	inverted := h.openWorkshop(token, board.ShotIDs[0], 3, -time.Hour)
	if inverted.Status != http.StatusBadRequest {
		t.Fatalf("inverted window accepted with status %d", inverted.Status)
	}
	tooLong := h.openWorkshop(token, board.ShotIDs[0], 3, 200*24*time.Hour)
	if tooLong.Status != http.StatusBadRequest {
		t.Fatalf("200 day window accepted with status %d", tooLong.Status)
	}
	tooManySeats := h.openWorkshop(token, board.ShotIDs[0], 500, 24*time.Hour)
	if tooManySeats.Status != http.StatusBadRequest {
		t.Fatalf("500 seats accepted with status %d", tooManySeats.Status)
	}
}

func TestApprenticeEnrolsOnceAndPracticeNeedsASeat(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshop := h.openWorkshop(director, board.ShotIDs[0], 2, 48*time.Hour)
	if workshop.Status != http.StatusCreated {
		t.Fatalf("cannot open workshop: %s", workshop.Body)
	}
	workshopID := workshop.int64Field("id")

	withoutSeat := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "我按照提示词重画了四个分镜，节奏更紧凑了。"},
	})
	if withoutSeat.Status != http.StatusUnprocessableEntity {
		t.Fatalf("practice accepted without enrolment, status %d", withoutSeat.Status)
	}

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)

	duplicate := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	})
	if duplicate.Status != http.StatusConflict {
		t.Fatalf("second enrolment returned %d, want 409", duplicate.Status)
	}

	submitted := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "我按照提示词重画了四个分镜，节奏更紧凑了。"},
	}, http.StatusCreated)
	if submitted.stringField("state") != "pending" {
		t.Fatalf("submission state is %q, want pending", submitted.stringField("state"))
	}

	tooShort := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "太短"},
	})
	if tooShort.Status != http.StatusBadRequest {
		t.Fatalf("short practice accepted with status %d", tooShort.Status)
	}
}

// TestDuplicateEnrolmentDoesNotLeakASeat reproduces a double-click enrolment:
// the second attempt hits the per-apprentice unique constraint and must not
// consume a seat, otherwise the workshop reports a phantom seat and refuses the
// next real apprentice.
func TestDuplicateEnrolmentDoesNotLeakASeat(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	first := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshop := h.openWorkshop(director, board.ShotIDs[0], 2, 48*time.Hour)
	if workshop.Status != http.StatusCreated {
		t.Fatalf("cannot open workshop: %s", workshop.Body)
	}
	workshopID := workshop.int64Field("id")

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  first,
	}, http.StatusCreated)

	// The first apprentice double-clicks enrolment. The retry must be rejected
	// as a conflict and must not consume a second seat.
	duplicate := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  first,
	})
	if duplicate.Status != http.StatusConflict {
		t.Fatalf("duplicate enrolment returned %d, want 409: %s", duplicate.Status, duplicate.Body)
	}

	// A second, distinct apprentice must still be admitted because the workshop
	// still has a real free seat.
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  director,
		payload: map[string]string{
			"email":        "second-apprentice@manjuflow.test",
			"display_name": "第二位学员",
			"password":     "second-pass-2026",
			"role":         "apprentice",
		},
	}, http.StatusCreated)
	second := h.login("second-apprentice@manjuflow.test", "second-pass-2026")
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  second,
	}, http.StatusCreated)

	// The workshop is now genuinely full, so a third apprentice is refused.
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  director,
		payload: map[string]string{
			"email":        "third-apprentice@manjuflow.test",
			"display_name": "第三位学员",
			"password":     "third-pass-2026",
			"role":         "apprentice",
		},
	}, http.StatusCreated)
	third := h.login("third-apprentice@manjuflow.test", "third-pass-2026")
	full := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  third,
	})
	if full.Status != http.StatusTooManyRequests {
		t.Fatalf("third enrolment returned %d, want 429: %s", full.Status, full.Body)
	}

	// The enrolled counter must match the two real enrolment records, not the
	// three attempts that touched the workshop.
	listed := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/workshops?state=open,teaching",
		token:  director,
	}, http.StatusOK)
	items := listed.Decoded["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("listing returned %d workshops, want 1", len(items))
	}
	if int(items[0].(map[string]any)["enrolled"].(float64)) != 2 {
		t.Fatalf("workshop reports %v seats taken, want 2", items[0].(map[string]any)["enrolled"])
	}
}

func TestDirectorCannotSubmitPracticeAndApprenticeCannotGrade(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 48*time.Hour).int64Field("id")
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)
	submission := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "第一次练习：镜头节奏与原片对齐，光影仍偏亮。"},
	}, http.StatusCreated)

	directorPractice := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   director,
		payload: map[string]string{"body": "导演不应该提交学员练习，这里应当被拒绝。"},
	})
	if directorPractice.Status != http.StatusForbidden {
		t.Fatalf("director submitted practice, status %d", directorPractice.Status)
	}

	apprenticeReview := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submission.int64Field("id")) + "/review",
		token:   apprentice,
		payload: map[string]any{"score": 90},
	})
	if apprenticeReview.Status != http.StatusForbidden {
		t.Fatalf("apprentice graded a submission, status %d", apprenticeReview.Status)
	}
}

func TestReviewOutcomeFollowsThePassMarkAndNeedsFeedbackWhenReturned(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 48*time.Hour).int64Field("id")
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)
	submission := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "第二次练习：补足了雨夜反光，人物比例稳定。"},
	}, http.StatusCreated)
	submissionID := submission.int64Field("id")

	missingFeedback := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submissionID) + "/review",
		token:   director,
		payload: map[string]any{"score": 40},
	})
	if missingFeedback.Status != http.StatusBadRequest {
		t.Fatalf("failing score without feedback accepted, status %d", missingFeedback.Status)
	}

	outOfRange := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submissionID) + "/review",
		token:   director,
		payload: map[string]any{"score": 140, "feedback": "分数越界"},
	})
	if outOfRange.Status != http.StatusBadRequest {
		t.Fatalf("out of range score accepted, status %d", outOfRange.Status)
	}

	accepted := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submissionID) + "/review",
		token:   director,
		payload: map[string]any{"score": 78, "feedback": "节奏到位，注意反光层次"},
	}, http.StatusOK)
	if accepted.stringField("state") != "accepted" {
		t.Fatalf("submission state is %q, want accepted", accepted.stringField("state"))
	}

	repeated := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submissionID) + "/review",
		token:   director,
		payload: map[string]any{"score": 90},
	})
	if repeated.Status != http.StatusConflict {
		t.Fatalf("second review returned %d, want 409", repeated.Status)
	}
}

func TestWorkshopCannotCloseWhileSubmissionsAwaitReview(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 48*time.Hour).int64Field("id")
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)
	submission := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "第三次练习：整体节奏与分镜一致，等待点评。"},
	}, http.StatusCreated)

	earlyClose := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/close",
		token:  director,
	})
	if earlyClose.Status != http.StatusUnprocessableEntity {
		t.Fatalf("teaching workshop closed directly, status %d", earlyClose.Status)
	}

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/grading",
		token:  director,
	}, http.StatusOK)

	blocked := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/close",
		token:  director,
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("workshop closed with a pending review, status %d", blocked.Status)
	}
	details, _ := blocked.Decoded["error"].(map[string]any)
	detail, _ := details["details"].(map[string]any)
	if detail["pending_reviews"] != "1" {
		t.Fatalf("refusal does not report the pending review count: %#v", detail)
	}

	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submission.int64Field("id")) + "/review",
		token:   director,
		payload: map[string]any{"score": 88, "feedback": "可以进入下一课"},
	}, http.StatusOK)

	closed := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/close",
		token:  director,
	}, http.StatusOK)
	if closed.stringField("state") != "closed" {
		t.Fatalf("workshop state is %q, want closed", closed.stringField("state"))
	}
}

func TestSubmissionWindowClosesAtTheDeadline(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 2*time.Hour).int64Field("id")
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)

	h.clock.Advance(3 * time.Hour)
	// The original bearer token expired together with the teaching window, so the
	// deadline itself has to be proven with a fresh session.
	apprentice = h.apprenticeToken()

	late := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "迟到的练习提交，按照截止时间规则应当被拒绝，这里只验证边界。"},
	})
	if late.Status != http.StatusUnprocessableEntity {
		t.Fatalf("late practice accepted, status %d", late.Status)
	}

	lateEnrol := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  h.login("late@manjuflow.test", registerLate(t, h, h.directorToken())),
	})
	if lateEnrol.Status != http.StatusUnprocessableEntity {
		t.Fatalf("late enrolment accepted, status %d", lateEnrol.Status)
	}
}

func registerLate(t *testing.T, h *harness, director string) string {
	t.Helper()
	const password = "late-comer-2026"
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  director,
		payload: map[string]string{
			"email":        "late@manjuflow.test",
			"display_name": "迟到学员",
			"password":     password,
			"role":         "apprentice",
		},
	}, http.StatusCreated)
	return password
}

func TestRetiringAPromptVersionIsBlockedWhileWorkAndTeachingDependOnIt(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := h.seedStoryboard(director, 1)

	blockedByShot := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  director,
	})
	if blockedByShot.Status != http.StatusUnprocessableEntity {
		t.Fatalf("retirement succeeded while a shot was bound, status %d", blockedByShot.Status)
	}
	references := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/references",
		token:  director,
	}, http.StatusOK)
	if int(references.Decoded["bound_shots"].(float64)) != 1 {
		t.Fatalf("reference report shows %v bound shots, want 1", references.Decoded["bound_shots"])
	}

	renderAndApprove(t, h, director, board, 0, true)
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  director,
	}, http.StatusOK)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 24*time.Hour).int64Field("id")
	blockedByWorkshop := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  director,
	})
	if blockedByWorkshop.Status != http.StatusUnprocessableEntity {
		t.Fatalf("retirement succeeded while a workshop was live, status %d", blockedByWorkshop.Status)
	}

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)
	submission := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "练习完成：按照同一版提示词复刻了夜市追逐的四个分镜，等待导演点评后结课。"},
	}, http.StatusCreated)
	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/practice-submissions/" + itoa(submission.int64Field("id")) + "/review",
		token:   director,
		payload: map[string]any{"score": 92, "feedback": "达到结课标准"},
	}, http.StatusOK)
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/grading",
		token:  director,
	}, http.StatusOK)
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/close",
		token:  director,
	}, http.StatusOK)

	retired := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  director,
	}, http.StatusOK)
	if retired.stringField("status") != "retired" {
		t.Fatalf("prompt version status is %q, want retired", retired.stringField("status"))
	}
	if retired.stringField("retired_at") == "" {
		t.Fatal("retired version carries no retirement timestamp")
	}
}

func TestApprovedShotCannotReturnToReworkWhileAWorkshopUsesIt(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	board := publishedBoard(t, h, director, 1)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 24*time.Hour).int64Field("id")

	blocked := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/review",
		token:   director,
		payload: map[string]any{"approve": false, "reason": "想换一版光影"},
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("shot returned to rework during teaching, status %d", blocked.Status)
	}
	details, _ := blocked.Decoded["error"].(map[string]any)
	detail, _ := details["details"].(map[string]any)
	if detail["live_workshops"] != "1" {
		t.Fatalf("refusal does not report the live workshop count: %#v", detail)
	}

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/grading",
		token:  director,
	}, http.StatusOK)
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/close",
		token:  director,
	}, http.StatusOK)

	allowed := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/review",
		token:   director,
		payload: map[string]any{"approve": false, "reason": "课程结束后重做光影"},
	}, http.StatusOK)
	if allowed.stringField("state") != "rework" {
		t.Fatalf("shot state is %q, want rework", allowed.stringField("state"))
	}
}

func TestOverdueWorkshopIsMovedToGradingBySweep(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()
	board := publishedBoard(t, h, director, 1)

	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 2*time.Hour).int64Field("id")
	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
		token:  apprentice,
	}, http.StatusCreated)
	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/workshops/" + itoa(workshopID) + "/submissions",
		token:   apprentice,
		payload: map[string]string{"body": "在截止时间之前提交的练习，等待截止之后由后台巡检自动进入评分阶段。"},
	}, http.StatusCreated)

	h.clock.Advance(3 * time.Hour)

	moved, err := h.app.Services.Teaching.SweepOverdue(context.Background(), 10)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if moved != 1 {
		t.Fatalf("sweep moved %d workshops, want 1", moved)
	}

	listed := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/workshops?state=grading",
		token:  h.directorToken(),
	}, http.StatusOK)
	if int(listed.Decoded["total"].(float64)) != 1 {
		t.Fatalf("grading listing total is %v, want 1", listed.Decoded["total"])
	}
}
