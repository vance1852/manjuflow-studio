package apptest

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/config"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

func TestConcurrentSubmissionsNeverOversellTheDailyQuota(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) { cfg.DailyRenderCapacity = 3 })
	token := h.directorToken()
	board := h.seedStoryboard(token, 8)

	var (
		start    = make(chan struct{})
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
		refused  int
	)
	for _, shotID := range board.ShotIDs {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			<-start
			reply := h.submitRender(token, id, "")
			mu.Lock()
			defer mu.Unlock()
			switch reply.Status {
			case http.StatusAccepted:
				accepted++
			case http.StatusTooManyRequests:
				refused++
			default:
				t.Errorf("unexpected submission status %d: %s", reply.Status, reply.Body)
			}
		}(shotID)
	}
	close(start)
	wg.Wait()

	if accepted != 3 {
		t.Fatalf("%d submissions were accepted, want exactly the 3 available slots", accepted)
	}
	if refused != len(board.ShotIDs)-3 {
		t.Fatalf("%d submissions were refused, want %d", refused, len(board.ShotIDs)-3)
	}

	quota := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if int(quota.Decoded["used"].(float64)) != 3 {
		t.Fatalf("quota ledger reports used=%v, want 3", quota.Decoded["used"])
	}
	if int(quota.Decoded["remaining"].(float64)) != 0 {
		t.Fatalf("quota ledger reports remaining=%v, want 0", quota.Decoded["remaining"])
	}
}

func TestConcurrentEnrolmentsRespectTheSeatLimit(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	board := publishedBoard(t, h, director, 1)
	workshopID := h.openWorkshop(director, board.ShotIDs[0], 2, 48*time.Hour).int64Field("id")

	const learners = 5
	tokens := make([]string, 0, learners)
	for i := 0; i < learners; i++ {
		email := "learner" + itoa(int64(i)) + "@manjuflow.test"
		password := "learner-pass-2026"
		h.mustCall(requestSpec{
			method: http.MethodPost,
			path:   "/v1/members",
			token:  director,
			payload: map[string]string{
				"email":        email,
				"display_name": "学员 " + itoa(int64(i)),
				"password":     password,
				"role":         "apprentice",
			},
		}, http.StatusCreated)
		tokens = append(tokens, h.login(email, password))
	}

	var (
		start    = make(chan struct{})
		wg       sync.WaitGroup
		mu       sync.Mutex
		seated   int
		rejected int
	)
	for _, learner := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			<-start
			reply := h.call(requestSpec{
				method: http.MethodPost,
				path:   "/v1/workshops/" + itoa(workshopID) + "/enrollments",
				token:  token,
			})
			mu.Lock()
			defer mu.Unlock()
			switch reply.Status {
			case http.StatusCreated:
				seated++
			case http.StatusTooManyRequests, http.StatusUnprocessableEntity:
				rejected++
			default:
				t.Errorf("unexpected enrolment status %d: %s", reply.Status, reply.Body)
			}
		}(learner)
	}
	close(start)
	wg.Wait()

	if seated != 2 {
		t.Fatalf("%d apprentices took a seat, want exactly the 2 available", seated)
	}
	if rejected != learners-2 {
		t.Fatalf("%d apprentices were rejected, want %d", rejected, learners-2)
	}

	listed := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/workshops?state=open,teaching",
		token:  director,
	}, http.StatusOK)
	workshops := listed.Decoded["items"].([]any)
	if len(workshops) != 1 {
		t.Fatalf("listing returned %d workshops, want 1", len(workshops))
	}
	if int(workshops[0].(map[string]any)["enrolled"].(float64)) != 2 {
		t.Fatalf("workshop reports %v seats taken, want 2", workshops[0].(map[string]any)["enrolled"])
	}
}

func TestConcurrentWorkersLeaseDistinctJobs(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) { cfg.DailyRenderCapacity = 4 })
	token := h.directorToken()
	board := h.seedStoryboard(token, 4)
	for _, shotID := range board.ShotIDs {
		if reply := h.submitRender(token, shotID, ""); reply.Status != http.StatusAccepted {
			t.Fatalf("submission returned %d: %s", reply.Status, reply.Body)
		}
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		leased = map[int64]string{}
	)
	for index := 0; index < 3; index++ {
		owner := "worker-" + itoa(int64(index))
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			for {
				assignment, err := h.app.Services.Render.Claim(context.Background(), owner)
				if err != nil {
					return
				}
				mu.Lock()
				if previous, exists := leased[assignment.Job.ID]; exists {
					t.Errorf("job %d was leased by both %s and %s", assignment.Job.ID, previous, owner)
				}
				leased[assignment.Job.ID] = owner
				mu.Unlock()
				if err := h.app.Services.Render.Complete(context.Background(), assignment, owner,
					"manju://concurrent/"+itoa(assignment.Job.ID)); err != nil {
					t.Errorf("worker %s cannot publish job %d: %v", owner, assignment.Job.ID, err)
					return
				}
			}
		}(owner)
	}
	wg.Wait()

	if len(leased) != 4 {
		t.Fatalf("%d jobs were processed, want 4", len(leased))
	}
	if state := h.seriesState(token, board.SeriesID); state != "reviewing" {
		t.Fatalf("series is %q, want reviewing after every shot rendered", state)
	}
}

func TestStaleSeriesWriteIsRejectedByTheVersionGuard(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	ctx := context.Background()
	loaded, err := h.app.Repositories.Series.FindByID(ctx, h.app.DB.Reader(), board.SeriesID)
	if err != nil {
		t.Fatalf("cannot load series: %v", err)
	}

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}

	stale := loaded
	stale.Title = "并发覆盖的标题"
	stale.UpdatedAt = h.clock.Now()
	writeErr := h.app.DB.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		return h.app.Repositories.Series.Save(ctx, q, stale)
	})
	if writeErr == nil {
		t.Fatal("stale series write was accepted")
	}
	if !apperr.IsCode(writeErr, apperr.CodeConflict) {
		t.Fatalf("stale write failed with %v, want a conflict", writeErr)
	}

	detail := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/series/" + itoa(board.SeriesID), token: token}, http.StatusOK)
	series := detail.Decoded["series"].(map[string]any)
	if series["title"] == "并发覆盖的标题" {
		t.Fatal("stale write overwrote the committed title")
	}
	if series["state"] != string(production.StateShooting) {
		t.Fatalf("series state is %v, want shooting", series["state"])
	}
}

func TestBusinessSequenceIssuesUniqueCodesUnderConcurrency(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	const attempts = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = map[string]bool{}
	)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply := h.call(requestSpec{
				method:  http.MethodPost,
				path:    "/v1/series",
				token:   token,
				payload: map[string]string{"title": "并发剧集", "code_prefix": "MJ"},
			})
			if reply.Status != http.StatusCreated {
				t.Errorf("series creation returned %d: %s", reply.Status, reply.Body)
				return
			}
			code := reply.stringField("code")
			mu.Lock()
			defer mu.Unlock()
			if codes[code] {
				t.Errorf("series code %s was issued twice", code)
			}
			codes[code] = true
		}()
	}
	wg.Wait()

	if len(codes) != attempts {
		t.Fatalf("%d distinct codes were issued, want %d", len(codes), attempts)
	}
}
