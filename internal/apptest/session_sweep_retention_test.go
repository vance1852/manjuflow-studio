package apptest

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/worker"
)

func TestRoutineSweepKeepsActiveSessionsSignedIn(t *testing.T) {
	h := newHarness(t)
	director := h.directorToken()
	apprentice := h.apprenticeToken()

	sweeper := worker.NewSweeper(worker.SweepOptions{
		Renders:  h.app.Services.Render,
		Teaching: h.app.Services.Teaching,
		Auth:     h.app.Services.Auth,
		Logger:   logging.New(io.Discard, logging.LevelError),
		Interval: time.Second,
	})

	// 两人登录几分钟后，后台巡检照例跑一轮。
	h.clock.Advance(5 * time.Minute)
	report, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("例行巡检失败: %v", err)
	}
	if report.PrunedSessions != 0 {
		t.Fatalf("例行巡检清理了 %d 个会话，此时没有任何会话过期", report.PrunedSessions)
	}

	stillDirector := h.call(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: director})
	if stillDirector.Status != http.StatusOK {
		t.Fatalf("巡检之后导演的 token 返回 %d，登录状态不应被清掉: %s", stillDirector.Status, stillDirector.Body)
	}
	stillApprentice := h.call(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: apprentice})
	if stillApprentice.Status != http.StatusOK {
		t.Fatalf("巡检之后学员的 token 返回 %d，登录状态不应被清掉: %s", stillApprentice.Status, stillApprentice.Body)
	}

	h.clock.Advance(h.cfg.SessionTTL + time.Minute)
	expiredReport, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("过期之后的巡检失败: %v", err)
	}
	if expiredReport.PrunedSessions < 2 {
		t.Fatalf("过期之后巡检只清理了 %d 个会话，两个会话都应当被清掉", expiredReport.PrunedSessions)
	}
	afterExpiry := h.call(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: director})
	if afterExpiry.Status != http.StatusUnauthorized {
		t.Fatalf("过期会话被清理后仍返回 %d，应当是未认证", afterExpiry.Status)
	}

	refreshed := h.directorToken()
	if session := h.call(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: refreshed}); session.Status != http.StatusOK {
		t.Fatalf("重新登录后的 token 返回 %d，应当可用: %s", session.Status, session.Body)
	}
}
