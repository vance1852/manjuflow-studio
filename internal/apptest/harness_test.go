package apptest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/app"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/config"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/worker"
)

const (
	directorEmail      = "director@manjuflow.test"
	directorPassword   = "director-pass-2026"
	apprenticeEmail    = "apprentice@manjuflow.test"
	apprenticePassword = "apprentice-pass-2026"
	studioSlug         = "solo-studio"
)

// harness owns one application instance backed by a real SQLite file.
type harness struct {
	t     *testing.T
	app   *app.App
	clock *clock.Fixed
	dir   string
	cfg   config.Config
}

func baseConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg := config.Config{
		HTTPAddr:            "127.0.0.1:0",
		DatabasePath:        filepath.Join(dir, "manjuflow.sqlite"),
		LogLevel:            "error",
		ShutdownGrace:       2 * time.Second,
		RequestTimeout:      10 * time.Second,
		SessionTTL:          2 * time.Hour,
		RenderLeaseTTL:      30 * time.Second,
		RenderPollInterval:  10 * time.Millisecond,
		RenderMaxAttempts:   3,
		RenderBackoffBase:   2 * time.Second,
		DailyRenderCapacity: 6,
		WorkshopSweepEvery:  time.Second,
		StudioSlug:          studioSlug,
		StudioName:          "独立漫剧工坊",
		DirectorEmail:       directorEmail,
		DirectorPassword:    directorPassword,
		ApprenticeEmail:     apprenticeEmail,
		ApprenticePassword:  apprenticePassword,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test configuration is invalid: %v", err)
	}
	return cfg
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, func(cfg *config.Config) {})
}

func newHarnessWith(t *testing.T, mutate func(cfg *config.Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := baseConfig(t, dir)
	mutate(&cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mutated configuration is invalid: %v", err)
	}
	fixed := clock.NewFixed(time.Date(2026, 3, 2, 9, 0, 0, 0, clock.MustBusinessLocation()))
	logger := logging.New(io.Discard, logging.LevelError)
	instance, err := app.Build(context.Background(), cfg, logger, fixed)
	if err != nil {
		t.Fatalf("cannot build application: %v", err)
	}
	h := &harness{t: t, app: instance, clock: fixed, dir: dir, cfg: cfg}
	t.Cleanup(func() {
		if closeErr := instance.Close(); closeErr != nil {
			t.Errorf("cannot close application: %v", closeErr)
		}
	})
	return h
}

// reopen closes the current instance and builds a new one on the same database
// file, which is how the suite proves restart recovery.
func (h *harness) reopen() *harness {
	h.t.Helper()
	if err := h.app.Close(); err != nil {
		h.t.Fatalf("cannot close application before reopen: %v", err)
	}
	logger := logging.New(io.Discard, logging.LevelError)
	instance, err := app.Build(context.Background(), h.cfg, logger, h.clock)
	if err != nil {
		h.t.Fatalf("cannot reopen application: %v", err)
	}
	next := &harness{t: h.t, app: instance, clock: h.clock, dir: h.dir, cfg: h.cfg}
	h.t.Cleanup(func() { _ = instance.Close() })
	return next
}

// response is a decoded HTTP reply.
type response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Decoded map[string]any
}

// errorCode extracts the stable error code from the unified envelope.
func (r response) errorCode() string {
	raw, ok := r.Decoded["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := raw["code"].(string)
	return code
}

func (r response) requestID() string {
	raw, ok := r.Decoded["error"].(map[string]any)
	if !ok {
		return r.Header.Get("X-Request-Id")
	}
	id, _ := raw["request_id"].(string)
	return id
}

func (r response) int64Field(key string) int64 {
	value, ok := r.Decoded[key].(float64)
	if !ok {
		return 0
	}
	return int64(value)
}

func (r response) stringField(key string) string {
	value, _ := r.Decoded[key].(string)
	return value
}

type requestSpec struct {
	method  string
	path    string
	token   string
	payload any
	headers map[string]string
}

func (h *harness) call(spec requestSpec) response {
	h.t.Helper()
	var body io.Reader
	if spec.payload != nil {
		encoded, err := json.Marshal(spec.payload)
		if err != nil {
			h.t.Fatalf("cannot encode payload: %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(spec.method, spec.path, body)
	if spec.payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if spec.token != "" {
		request.Header.Set("Authorization", "Bearer "+spec.token)
	}
	for key, value := range spec.headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(recorder, request)

	result := response{Status: recorder.Code, Header: recorder.Header(), Body: recorder.Body.Bytes()}
	if len(result.Body) > 0 {
		decoded := map[string]any{}
		if err := json.Unmarshal(result.Body, &decoded); err == nil {
			result.Decoded = decoded
		}
	}
	return result
}

func (h *harness) login(email, password string) string {
	h.t.Helper()
	reply := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/auth/login",
		payload: map[string]string{"studio": studioSlug, "email": email, "password": password},
	})
	if reply.Status != http.StatusOK {
		h.t.Fatalf("login for %s failed with %d: %s", email, reply.Status, reply.Body)
	}
	token := reply.stringField("token")
	if token == "" {
		h.t.Fatalf("login for %s returned no token: %s", email, reply.Body)
	}
	return token
}

func (h *harness) directorToken() string   { return h.login(directorEmail, directorPassword) }
func (h *harness) apprenticeToken() string { return h.login(apprenticeEmail, apprenticePassword) }

// mustCall asserts the expected status and returns the decoded reply.
func (h *harness) mustCall(spec requestSpec, expected int) response {
	h.t.Helper()
	reply := h.call(spec)
	if reply.Status != expected {
		h.t.Fatalf("%s %s returned %d, want %d: %s", spec.method, spec.path, reply.Status, expected, reply.Body)
	}
	return reply
}

// storyboard is the fixture graph most pipeline tests need.
type storyboard struct {
	TemplateID int64
	VersionID  int64
	SeriesID   int64
	SeriesCode string
	ShotIDs    []int64
}

// seedStoryboard creates one template with an active version, one series and the
// requested number of bound shots.
func (h *harness) seedStoryboard(token string, shots int) storyboard {
	h.t.Helper()
	template := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates",
		token:  token,
		payload: map[string]string{
			"slug":       "night-market-chase",
			"title":      "夜市追逐分镜提示词",
			"discipline": "storyboard",
		},
	}, http.StatusCreated)

	version := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates/" + itoa(template.int64Field("id")) + "/versions",
		token:  token,
		payload: map[string]any{
			"body":     PromptBody("夜市追逐的女主角", "赛博水墨", 4),
			"notes":    "初版",
			"activate": true,
		},
	}, http.StatusCreated)

	series := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series",
		token:  token,
		payload: map[string]string{
			"title":       "夜市追逐",
			"logline":     "一个人导演的三分钟漫剧",
			"code_prefix": "MJ",
		},
	}, http.StatusCreated)

	drafts := make([]map[string]any, 0, shots)
	for i := 1; i <= shots; i++ {
		drafts = append(drafts, map[string]any{
			"ordinal":   i,
			"title":     "分镜 " + itoa(int64(i)),
			"direction": "镜头推进，保持人物比例",
		})
	}
	planned := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/series/" + itoa(series.int64Field("id")) + "/shots",
		token:   token,
		payload: map[string]any{"shots": drafts},
	}, http.StatusCreated)

	board := storyboard{
		TemplateID: template.int64Field("id"),
		VersionID:  version.int64Field("id"),
		SeriesID:   series.int64Field("id"),
		SeriesCode: series.stringField("code"),
	}
	results, ok := planned.Decoded["results"].([]any)
	if !ok {
		h.t.Fatalf("planning response has no results: %s", planned.Body)
	}
	for _, item := range results {
		entry, ok := item.(map[string]any)
		if !ok {
			h.t.Fatalf("unexpected planning entry %#v", item)
		}
		id, ok := entry["shot_id"].(float64)
		if !ok {
			h.t.Fatalf("planning entry has no shot id: %#v", entry)
		}
		board.ShotIDs = append(board.ShotIDs, int64(id))
	}
	for _, shotID := range board.ShotIDs {
		h.mustCall(requestSpec{
			method:  http.MethodPost,
			path:    "/v1/shots/" + itoa(shotID) + "/prompt",
			token:   token,
			payload: map[string]any{"prompt_version_id": board.VersionID},
		}, http.StatusOK)
	}
	return board
}

func (h *harness) submitRender(token string, shotID int64, key string) response {
	h.t.Helper()
	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(shotID) + "/render",
		token:   token,
		headers: headers,
	})
}

func (h *harness) shotState(token string, board storyboard, ordinal int) string {
	h.t.Helper()
	detail := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series/" + itoa(board.SeriesID),
		token:  token,
	}, http.StatusOK)
	shots, ok := detail.Decoded["shots"].([]any)
	if !ok {
		h.t.Fatalf("series detail has no shots: %s", detail.Body)
	}
	for _, item := range shots {
		shot, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if int(shot["ordinal"].(float64)) == ordinal {
			state, _ := shot["state"].(string)
			return state
		}
	}
	h.t.Fatalf("series has no shot with ordinal %d", ordinal)
	return ""
}

func (h *harness) seriesState(token string, seriesID int64) string {
	h.t.Helper()
	detail := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series/" + itoa(seriesID),
		token:  token,
	}, http.StatusOK)
	series, ok := detail.Decoded["series"].(map[string]any)
	if !ok {
		h.t.Fatalf("series detail is malformed: %s", detail.Body)
	}
	state, _ := series["state"].(string)
	return state
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }

// processRenders drains the render queue with the given renderer and reports how
// many cycles found work.
func (h *harness) processRenders(renderer worker.Renderer, maxCycles int) int {
	h.t.Helper()
	instance := worker.NewRenderWorker(worker.RenderOptions{
		Name:     "test-render-worker",
		Renders:  h.app.Services.Render,
		Renderer: renderer,
		Logger:   logging.New(io.Discard, logging.LevelError),
		Interval: time.Millisecond,
	})
	processed := 0
	for i := 0; i < maxCycles; i++ {
		worked, err := instance.ProcessOne(context.Background())
		if err != nil {
			h.t.Fatalf("render cycle %d failed: %v", i, err)
		}
		if !worked {
			break
		}
		processed++
	}
	return processed
}

// renderAndApprove submits, renders and optionally approves one shot.
func renderAndApprove(t *testing.T, h *harness, token string, board storyboard, index int, approve bool) {
	t.Helper()
	if reply := h.submitRender(token, board.ShotIDs[index], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission for shot %d returned %d: %s", index, reply.Status, reply.Body)
	}
	if processed := h.processRenders(worker.StoryboardRenderer{}, 4); processed == 0 {
		t.Fatalf("render worker found no work for shot %d", index)
	}
	if !approve {
		return
	}
	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[index]) + "/review",
		token:   token,
		payload: map[string]any{"approve": true},
	}, http.StatusOK)
}
