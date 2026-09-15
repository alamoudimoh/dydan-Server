package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newSessionsTestRouter wires the real JWT middleware plus the session
// endpoints exactly as Routes does, against an in-memory database.
func newSessionsTestRouter(t *testing.T) (*gin.Engine, *TokenService, *gorm.DB, *uModel.User, *uModel.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	svc, dbh, userA := newTokenServiceHarness(t)
	db := dbh
	userB := &uModel.User{Username: "sessions-user-b", CircleID: 2}
	if err := db.Create(userB).Error; err != nil {
		t.Fatalf("seed user b: %v", err)
	}

	handler := NewAuthHandler(svc, svc.userRepo, svc.jwtAuth, nil)

	r := gin.New()
	protected := r.Group("api/v1/auth")
	protected.Use(svc.jwtAuth.MiddlewareFunc())
	{
		protected.GET("sessions", handler.ListSessionsHandler)
		protected.DELETE("sessions/:id", handler.RevokeSessionHandler)
		protected.POST("sessions/revoke-others", handler.RevokeOtherSessionsHandler)
	}
	return r, svc, db, userA, userB
}

func doAuthed(r *gin.Engine, method, target, accessToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func loginFor(t *testing.T, svc *TokenService, user *uModel.User) *TokenResponse {
	t.Helper()
	resp, err := svc.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return resp
}

type listedSession struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"createdAt"`
	LastActiveAt string `json:"lastActiveAt"`
	ExpiresAt    string `json:"expiresAt"`
	Current      bool   `json:"current"`
}

func listSessions(t *testing.T, r *gin.Engine, token string) []listedSession {
	t.Helper()
	w := doAuthed(r, http.MethodGet, "/api/v1/auth/sessions", token)
	if w.Code != http.StatusOK {
		t.Fatalf("list sessions: got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Res struct {
			Sessions []listedSession `json:"sessions"`
		} `json:"res"`
		Raw json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list response: %v (%s)", err, w.Body.String())
	}
	// Credential hygiene: no secret material anywhere in the payload.
	raw := w.Body.String()
	for _, forbidden := range []string{"token_hash", "tokenHash", "refresh_token", "accessToken"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("session listing must never expose %q", forbidden)
		}
	}
	return body.Res.Sessions
}

func TestListSessionsFlagsCurrentAndIsolatesUsers(t *testing.T) {
	r, svc, _, userA, userB := newSessionsTestRouter(t)

	a1 := loginFor(t, svc, userA)
	a2 := loginFor(t, svc, userA)
	b1 := loginFor(t, svc, userB)

	sessions := listSessions(t, r, a1.AccessToken)
	if len(sessions) != 2 {
		t.Fatalf("user A should see 2 own sessions, got %d", len(sessions))
	}
	currentCount := 0
	var currentID string
	for _, s := range sessions {
		if s.Current {
			currentCount++
			currentID = s.ID
		}
	}
	if currentCount != 1 || currentID == "" {
		t.Fatalf("exactly one session must be flagged current, got %d", currentCount)
	}
	// The flagged one is the family of the token used to call the endpoint.
	_, famA1 := svc.SessionRefForToken(context.Background(), a1.RefreshToken)
	if currentID != famA1 {
		t.Fatalf("current flag points at %q, want caller's family %q", currentID, famA1)
	}

	// User B sees only their own session.
	bSessions := listSessions(t, r, b1.AccessToken)
	if len(bSessions) != 1 {
		t.Fatalf("user B should see 1 own session, got %d", len(bSessions))
	}
	_, famB1 := svc.SessionRefForToken(context.Background(), b1.RefreshToken)
	if bSessions[0].ID != famB1 {
		t.Fatalf("user B session id mismatch")
	}
	if bSessions[0].Current != true {
		t.Fatal("sole session must be current for its owner")
	}

	_ = a2
}

func TestUnauthenticatedSessionAccessRejected(t *testing.T) {
	r, _, _, _, _ := newSessionsTestRouter(t)
	if w := doAuthed(r, http.MethodGet, "/api/v1/auth/sessions", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer token must be rejected, got %d", w.Code)
	}
	if w := doAuthed(r, http.MethodDelete, "/api/v1/auth/sessions/some-family", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer token must be rejected, got %d", w.Code)
	}
}

func TestRevokeSelectedSessionEnforcesOwnership(t *testing.T) {
	r, svc, db, userA, userB := newSessionsTestRouter(t)
	ctx := context.Background()

	aLogin := loginFor(t, svc, userA)
	aOther := loginFor(t, svc, userA)
	bLogin := loginFor(t, svc, userB)

	_, famA := svc.SessionRefForToken(ctx, aLogin.RefreshToken)
	_, famAOther := svc.SessionRefForToken(ctx, aOther.RefreshToken)
	_, famB := svc.SessionRefForToken(ctx, bLogin.RefreshToken)

	// A cannot revoke B's session.
	w := doAuthed(r, http.MethodDelete, "/api/v1/auth/sessions/"+famB, aLogin.AccessToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("revoking another user's session must 404 (no existence leak), got %d", w.Code)
	}
	if liveFamilySessions(t, db, famB) == 0 {
		t.Fatal("foreign session must remain untouched")
	}

	// Unknown ids also 404.
	w = doAuthed(r, http.MethodDelete, "/api/v1/auth/sessions/nonexistent-family", aLogin.AccessToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown session must 404, got %d", w.Code)
	}

	// Revoking another one of A's own sessions works and kills the family.
	w = doAuthed(r, http.MethodDelete, "/api/v1/auth/sessions/"+famAOther, aLogin.AccessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("revoking own session failed: %d %s", w.Code, w.Body.String())
	}
	if liveFamilySessions(t, db, famAOther) != 0 {
		t.Fatal("selected family must be fully revoked")
	}
	// Its refresh token no longer refreshes.
	if _, err := svc.RefreshTokens(ctx, aOther.RefreshToken); err == nil {
		t.Fatal("refresh with revoked family token must fail")
	}
	// Caller's own session survives.
	if _, err := svc.RefreshTokens(ctx, aLogin.RefreshToken); err != nil {
		t.Fatalf("caller's own session must survive: %v", err)
	}

	// Revoking your own current session still succeeds (self logout).
	w = doAuthed(r, http.MethodDelete, "/api/v1/auth/sessions/"+famA, aLogin.AccessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("self-revocation should succeed: %d", w.Code)
	}
}

func TestRevokeOthersKeepsOnlyCallerSession(t *testing.T) {
	r, svc, db, userA, _ := newSessionsTestRouter(t)
	ctx := context.Background()

	aKeep := loginFor(t, svc, userA)
	aKill1 := loginFor(t, svc, userA)
	aKill2 := loginFor(t, svc, userA)

	_, famKeep := svc.SessionRefForToken(ctx, aKeep.RefreshToken)

	w := doAuthed(r, http.MethodPost, "/api/v1/auth/sessions/revoke-others", aKeep.AccessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke-others failed: %d %s", w.Code, w.Body.String())
	}

	if liveFamilySessions(t, db, famKeep) == 0 {
		t.Fatal("caller's family must survive revoke-others")
	}
	for _, tok := range []string{aKill1.RefreshToken, aKill2.RefreshToken} {
		if _, err := svc.RefreshTokens(ctx, tok); err == nil {
			t.Fatal("other families must be revoked")
		}
	}
	if _, err := svc.RefreshTokens(ctx, aKeep.RefreshToken); err != nil {
		t.Fatalf("caller's refresh must keep working: %v", err)
	}

	// Listing afterwards shows exactly the current session.
	sessions := listSessions(t, r, aKeep.AccessToken)
	if len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("expected single current session after revoke-others, got %+v", sessions)
	}
}

// TestLegacyAccessTokenStillManagesSessions proves tokens without lineage
// claims (old integration logins) fall back to cookie/body-based current
// detection instead of being locked out of session management.
func TestLegacyAccessTokenStillManagesSessions(t *testing.T) {
	r, svc, _, userA, _ := newSessionsTestRouter(t)

	login := loginFor(t, svc, userA)
	legacyToken, _, err := svc.jwtAuth.TokenGenerator(&uModel.UserDetails{User: *userA})
	if err != nil {
		t.Fatalf("legacy token: %v", err)
	}
	_ = login

	// Without any lineage info the list still works; nothing is flagged.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+legacyToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy access token must access sessions, got %d", w.Code)
	}
	var body struct {
		Res struct {
			Sessions []listedSession `json:"sessions"`
		} `json:"res"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Res.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(body.Res.Sessions))
	}
	if body.Res.Sessions[0].Current {
		t.Fatal("without lineage claims no session may be falsely flagged current")
	}

}

// TestRevokeSessionByIDEndpointKillsOnlyThatSession proves the DELETE
// /auth/sessions/:id surface accepts an individual refresh-session row ID and
// revokes only that row: the revoked session's access JWT dies immediately,
// while sibling sessions in the same family keep working.
func TestRevokeSessionByIDEndpointKillsOnlyThatSession(t *testing.T) {
	r, svc, db, userA, _ := newSessionsTestRouter(t)

	first := loginFor(t, svc, userA)
	second := loginFor(t, svc, userA)
	third := loginFor(t, svc, userA)

	sidA, famA := svc.SessionRefForToken(context.Background(), first.RefreshToken)
	sidB, famB := svc.SessionRefForToken(context.Background(), second.RefreshToken)
	sidC, famC := svc.SessionRefForToken(context.Background(), third.RefreshToken)
	if famA == famB || famB == famC || famA == famC {
		t.Fatal("each login must create its own rotation family")
	}
	if sidA == "" || sidB == "" || sidC == "" {
		t.Fatal("session ids must resolve")
	}

	// Revoke session B by its row id through the HTTP endpoint.
	w := doAuthed(r, http.MethodDelete, "/api/v1/auth/sessions/"+sidB, third.AccessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke by session id: got %d: %s", w.Code, w.Body.String())
	}

	if _, _, ok := liveSessionRows(t, db, sidB); ok {
		t.Fatal("session B row still live after endpoint revoke")
	}
	for name, tok := range map[string]string{"A": first.AccessToken, "C": third.AccessToken} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("sibling session %s wrongly rejected after granular revoke: %d", name, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+second.AccessToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("revoked session B JWT still accepted after endpoint revoke")
	}
}
