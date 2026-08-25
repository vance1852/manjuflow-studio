package httpapi

import (
	"net/http"

	"github.com/vance1852/manjuflow-studio/internal/service/authsvc"
)

type loginRequest struct {
	Studio   string `json:"studio"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
	User      userView `json:"user"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var payload loginRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	studio := payload.Studio
	if studio == "" {
		studio = s.defaultStudio
	}
	result, err := s.auth.Login(r.Context(), authsvc.LoginInput{
		StudioSlug: studio,
		Email:      payload.Email,
		Password:   payload.Password,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Token:     result.Token,
		ExpiresAt: formatTime(result.ExpiresAt),
		User:      newUserView(result.User),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context()); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

type sessionResponse struct {
	SessionID    int64    `json:"session_id"`
	User         userView `json:"user"`
	Capabilities []string `json:"capabilities"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	current, err := s.auth.Describe(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	capabilities := make([]string, 0, len(current.Capabilities))
	for _, capability := range current.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		SessionID:    current.SessionID,
		User:         newUserView(current.User),
		Capabilities: capabilities,
	})
}

type createMemberRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

func (s *Server) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	var payload createMemberRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	user, err := s.auth.CreateMember(r.Context(), authsvc.CreateMemberInput{
		Email:       payload.Email,
		DisplayName: payload.DisplayName,
		Password:    payload.Password,
		Role:        payload.Role,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newUserView(user))
}
