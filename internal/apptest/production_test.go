package apptest

import (
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/config"
)

func TestPlanningBatchKeepsAcceptedShotsWhenOneOrdinalIsRejected(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	series := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/series",
		token:   token,
		payload: map[string]string{"title": "部分失败编排", "code_prefix": "MJ"},
	}, http.StatusCreated)
	seriesID := series.int64Field("id")

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(seriesID) + "/shots",
		token:  token,
		payload: map[string]any{"shots": []map[string]any{
			{"ordinal": 1, "title": "开场"},
		}},
	}, http.StatusCreated)

	mixed := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(seriesID) + "/shots",
		token:  token,
		payload: map[string]any{"shots": []map[string]any{
			{"ordinal": 1, "title": "重复序号"},
			{"ordinal": 0, "title": "非法序号"},
			{"ordinal": 2, "title": "追逐"},
			{"ordinal": 3, "title": ""},
			{"ordinal": 4, "title": "收尾"},
		}},
	}, http.StatusMultiStatus)

	if created := int(mixed.Decoded["created"].(float64)); created != 2 {
		t.Fatalf("batch created %d shots, want 2", created)
	}
	results := mixed.Decoded["results"].([]any)
	if len(results) != 5 {
		t.Fatalf("batch returned %d results, want 5 per-item outcomes", len(results))
	}
	for index, item := range results {
		entry := item.(map[string]any)
		created, _ := entry["created"].(bool)
		message, _ := entry["error"].(string)
		switch index {
		case 2, 4:
			if !created || message != "" {
				t.Fatalf("result %d should have been created: %#v", index, entry)
			}
		default:
			if created || message == "" {
				t.Fatalf("result %d should carry a rejection reason: %#v", index, entry)
			}
		}
	}

	detail := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/series/" + itoa(seriesID), token: token}, http.StatusOK)
	shots := detail.Decoded["shots"].([]any)
	if len(shots) != 3 {
		t.Fatalf("series holds %d shots, want 3 after the partial batch", len(shots))
	}
}

func TestDraftPromptVersionCannotBeBoundToAShot(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	draft := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions",
		token:  token,
		payload: map[string]any{
			"body":     PromptBody("草稿主角", "水墨", 2),
			"activate": false,
		},
	}, http.StatusCreated)

	blocked := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/prompt",
		token:   token,
		payload: map[string]any{"prompt_version_id": draft.int64Field("id")},
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("draft version was bound, status %d: %s", blocked.Status, blocked.Body)
	}
	if blocked.errorCode() != "failed_precondition" {
		t.Fatalf("unexpected error code %q", blocked.errorCode())
	}

	h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(draft.int64Field("id")) + "/activate",
		token:  token,
	}, http.StatusOK)
	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/prompt",
		token:   token,
		payload: map[string]any{"prompt_version_id": draft.int64Field("id")},
	}, http.StatusOK)
}

// versionStatus fetches the lifecycle state of one prompt version through the
// listing endpoint so tests can assert it never moved without the caller asking.
func versionStatus(t *testing.T, h *harness, token string, templateID, versionID int64) string {
	t.Helper()
	page := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-templates/" + itoa(templateID) + "/versions",
		token:  token,
	}, http.StatusOK)
	items, ok := page.Decoded["items"].([]any)
	if !ok {
		t.Fatalf("version listing has no items: %s", page.Body)
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if int64(entry["id"].(float64)) == versionID {
			status, _ := entry["status"].(string)
			return status
		}
	}
	t.Fatalf("version %d is missing from the template listing", versionID)
	return ""
}

func TestActivatingANewVersionLeavesABoundEarlierVersionUsable(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	// The shot is bound to the first version and a render is queued but not run,
	// mirroring the night-market chase board waiting in the render queue.
	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("render submission returned %d: %s", reply.Status, reply.Body)
	}

	references := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/references",
		token:  token,
	}, http.StatusOK)
	if int(references.Decoded["bound_shots"].(float64)) != 1 {
		t.Fatalf("version backs %v bound shots, want 1", references.Decoded["bound_shots"])
	}
	if int(references.Decoded["unfinished_jobs"].(float64)) != 1 {
		t.Fatalf("version backs %v unfinished jobs, want 1", references.Decoded["unfinished_jobs"])
	}

	// Append and activate the revised version while the first one is still in use.
	second := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions",
		token:  token,
		payload: map[string]any{
			"body":     PromptBody("夜市追逐的女主角", "赛博水墨改稿", 4),
			"notes":    "第二版",
			"activate": true,
		},
	}, http.StatusCreated)
	secondID := second.int64Field("id")

	// Activating the revision must not touch the bound first version.
	if status := versionStatus(t, h, token, board.TemplateID, board.VersionID); status != "active" {
		t.Fatalf("bound first version was moved to %q by activating the second", status)
	}
	if status := versionStatus(t, h, token, board.TemplateID, secondID); status != "active" {
		t.Fatalf("second version is %q, want active", status)
	}

	// Retirement of the in-use first version still goes through the reference
	// gate and must be refused, exactly like an explicit retire call.
	blocked := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  token,
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("retiring the in-use version returned %d, want a refusal: %s", blocked.Status, blocked.Body)
	}

	// The shot stays renderable on its original version without rebinding.
	// The queued job is still in flight, so a fresh submission is refused by the
	// in-flight guard rather than by a retired-version error. The refusal must
	// not carry the prompt version status, which is how a retired version would
	// surface.
	resubmit := h.submitRender(token, board.ShotIDs[0], "")
	if resubmit.Status != http.StatusUnprocessableEntity && resubmit.Status != http.StatusConflict {
		t.Fatalf("resubmission returned %d, want an in-flight refusal: %s", resubmit.Status, resubmit.Body)
	}
	errEnvelope, _ := resubmit.Decoded["error"].(map[string]any)
	detail, _ := errEnvelope["details"].(map[string]any)
	if _, retired := detail["prompt_version_status"]; retired {
		t.Fatalf("resubmission blamed the prompt version, the bound version must remain bindable: %s", resubmit.Body)
	}
}

func TestPromptBodyMustDeclareTheRequiredDirectives(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	reply := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions",
		token:   token,
		payload: map[string]any{"body": "只有一句没有指令的描述文字", "activate": true},
	})
	if reply.Status != http.StatusBadRequest {
		t.Fatalf("invalid prompt body accepted, status %d", reply.Status)
	}
	details, _ := reply.Decoded["error"].(map[string]any)
	detail, _ := details["details"].(map[string]any)
	if detail["missing_directive"] == nil {
		t.Fatalf("error envelope does not name the missing directive: %#v", detail)
	}
}

func TestSubmittingRenderConsumesQuotaAndMovesSeriesIntoShooting(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	before := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if int(before.Decoded["remaining"].(float64)) != h.cfg.DailyRenderCapacity {
		t.Fatalf("initial quota is %v, want %d", before.Decoded["remaining"], h.cfg.DailyRenderCapacity)
	}

	accepted := h.submitRender(token, board.ShotIDs[0], "")
	if accepted.Status != http.StatusAccepted {
		t.Fatalf("render submission returned %d: %s", accepted.Status, accepted.Body)
	}
	if accepted.stringField("state") != "queued" {
		t.Fatalf("job state is %q, want queued", accepted.stringField("state"))
	}

	after := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if int(after.Decoded["used"].(float64)) != 1 {
		t.Fatalf("quota used is %v, want 1", after.Decoded["used"])
	}
	if state := h.shotState(token, board, 1); state != "rendering" {
		t.Fatalf("shot state is %q, want rendering", state)
	}
	if state := h.seriesState(token, board.SeriesID); state != "shooting" {
		t.Fatalf("series state is %q, want shooting", state)
	}
}

func TestSecondRenderForTheSameShotIsRefusedWhileOneIsInFlight(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("first submission returned %d", reply.Status)
	}
	second := h.submitRender(token, board.ShotIDs[0], "")
	if second.Status != http.StatusUnprocessableEntity && second.Status != http.StatusConflict {
		t.Fatalf("second submission returned %d, want a refusal", second.Status)
	}

	quota := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if int(quota.Decoded["used"].(float64)) != 1 {
		t.Fatalf("refused submission still burned quota: used=%v", quota.Decoded["used"])
	}
}

func TestRepeatingAnIdempotentSubmissionReplaysTheFirstJob(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	first := h.submitRender(token, board.ShotIDs[0], "take-01")
	if first.Status != http.StatusAccepted {
		t.Fatalf("first submission returned %d: %s", first.Status, first.Body)
	}
	replay := h.submitRender(token, board.ShotIDs[0], "take-01")
	if replay.Status != http.StatusAccepted {
		t.Fatalf("replay returned %d: %s", replay.Status, replay.Body)
	}
	if replay.Header.Get("Idempotent-Replay") != "true" {
		t.Fatal("replay was not marked as idempotent")
	}
	if first.int64Field("job_id") != replay.int64Field("job_id") {
		t.Fatalf("replay created job %d instead of %d", replay.int64Field("job_id"), first.int64Field("job_id"))
	}

	quota := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if int(quota.Decoded["used"].(float64)) != 1 {
		t.Fatalf("replay consumed a second slot: used=%v", quota.Decoded["used"])
	}

	reusedKey := h.submitRender(token, board.ShotIDs[1], "take-01")
	if reusedKey.Status != http.StatusConflict {
		t.Fatalf("reusing the key for another shot returned %d, want 409", reusedKey.Status)
	}
}

func TestQuotaExhaustionRefusesFurtherRendersForTheDay(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) { cfg.DailyRenderCapacity = 2 })
	token := h.directorToken()
	board := h.seedStoryboard(token, 3)

	for i := 0; i < 2; i++ {
		if reply := h.submitRender(token, board.ShotIDs[i], ""); reply.Status != http.StatusAccepted {
			t.Fatalf("submission %d returned %d: %s", i, reply.Status, reply.Body)
		}
	}
	exhausted := h.submitRender(token, board.ShotIDs[2], "")
	if exhausted.Status != http.StatusTooManyRequests {
		t.Fatalf("third submission returned %d, want 429: %s", exhausted.Status, exhausted.Body)
	}
	if exhausted.errorCode() != "resource_exhausted" {
		t.Fatalf("unexpected error code %q", exhausted.errorCode())
	}
	if state := h.shotState(token, board, 3); state != "bound" {
		t.Fatalf("refused shot moved to %q, want bound", state)
	}
}

func TestPublishRequiresEveryShotApproved(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	tooEarly := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  token,
	})
	if tooEarly.Status != http.StatusUnprocessableEntity {
		t.Fatalf("draft series was published, status %d", tooEarly.Status)
	}

	renderAndApprove(t, h, token, board, 0, true)
	stillBlocked := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  token,
	})
	if stillBlocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("half approved series was published, status %d", stillBlocked.Status)
	}

	renderAndApprove(t, h, token, board, 1, true)
	published := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  token,
	}, http.StatusOK)
	if published.stringField("state") != "published" {
		t.Fatalf("series state is %q, want published", published.stringField("state"))
	}
	if published.stringField("published_at") == "" {
		t.Fatal("published series carries no publication timestamp")
	}
}

func TestSendingAShotBackToReworkReturnsTheSeriesToShooting(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	renderAndApprove(t, h, token, board, 0, false)
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("shot state is %q, want rendered", state)
	}

	reworked := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/review",
		token:   token,
		payload: map[string]any{"approve": false, "reason": "人物比例仍然不稳"},
	}, http.StatusOK)
	if reworked.stringField("state") != "rework" {
		t.Fatalf("shot state is %q, want rework", reworked.stringField("state"))
	}
	if reworked.stringField("artifact_ref") != "" {
		t.Fatal("rework kept the stale artifact reference")
	}
	if state := h.seriesState(token, board.SeriesID); state != "shooting" {
		t.Fatalf("series state is %q, want shooting", state)
	}

	missingReason := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/review",
		token:   token,
		payload: map[string]any{"approve": false},
	})
	if missingReason.Status != http.StatusUnprocessableEntity && missingReason.Status != http.StatusBadRequest {
		t.Fatalf("rework without a reason returned %d", missingReason.Status)
	}
}

func TestListingSeriesAppliesFilterSortAndPaginationConsistently(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	for i := 0; i < 5; i++ {
		h.mustCall(requestSpec{
			method: http.MethodPost,
			path:   "/v1/series",
			token:  token,
			payload: map[string]string{
				"title":       "列表剧集 " + itoa(int64(i)),
				"code_prefix": "MJ",
			},
		}, http.StatusCreated)
		h.clock.Advance(time.Minute)
	}

	firstPage := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?limit=2&offset=0&sort_by=code&order=asc",
		token:  token,
	}, http.StatusOK)
	if int(firstPage.Decoded["total"].(float64)) != 5 {
		t.Fatalf("total is %v, want 5", firstPage.Decoded["total"])
	}
	firstItems := firstPage.Decoded["items"].([]any)
	if len(firstItems) != 2 {
		t.Fatalf("first page holds %d items, want 2", len(firstItems))
	}

	secondPage := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?limit=2&offset=2&sort_by=code&order=asc",
		token:  token,
	}, http.StatusOK)
	secondItems := secondPage.Decoded["items"].([]any)
	if len(secondItems) != 2 {
		t.Fatalf("second page holds %d items, want 2", len(secondItems))
	}
	firstCode := firstItems[0].(map[string]any)["code"].(string)
	secondCode := secondItems[0].(map[string]any)["code"].(string)
	if firstCode >= secondCode {
		t.Fatalf("pages overlap or are unordered: %s then %s", firstCode, secondCode)
	}

	filtered := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?state=draft&limit=10",
		token:  token,
	}, http.StatusOK)
	if int(filtered.Decoded["total"].(float64)) != 5 {
		t.Fatalf("draft filter total is %v, want 5", filtered.Decoded["total"])
	}

	empty := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series?state=published&limit=10",
		token:  token,
	}, http.StatusOK)
	if int(empty.Decoded["total"].(float64)) != 0 {
		t.Fatalf("published filter total is %v, want 0", empty.Decoded["total"])
	}

	badSort := h.call(requestSpec{method: http.MethodGet, path: "/v1/series?sort_by=secret", token: token})
	if badSort.Status != http.StatusBadRequest {
		t.Fatalf("unknown sort key returned %d", badSort.Status)
	}
	badLimit := h.call(requestSpec{method: http.MethodGet, path: "/v1/series?limit=5000", token: token})
	if badLimit.Status != http.StatusBadRequest {
		t.Fatalf("oversized limit returned %d", badLimit.Status)
	}
}

func TestSeriesCodesAreUniquePerStudio(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		reply := h.mustCall(requestSpec{
			method:  http.MethodPost,
			path:    "/v1/series",
			token:   token,
			payload: map[string]string{"title": "序号剧集", "code_prefix": "MJ"},
		}, http.StatusCreated)
		code := reply.stringField("code")
		if code == "" {
			t.Fatal("series has no business code")
		}
		if seen[code] {
			t.Fatalf("series code %s was issued twice", code)
		}
		seen[code] = true
	}
}
