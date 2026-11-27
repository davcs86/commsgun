package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/password"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	errSession       = "session"
	errMode          = "mode"
	errSessionKind   = "session_kind"
	errCredentials   = "invalid_credentials"
	errNotLinked     = "not_linked"
	errIdentity      = "invalid_identity"
	errReauth        = "reauth_required"
	errAlreadyLinked = "already_linked"
)

var errUnavailable = errors.New("admin: password hashing unavailable")

// hasher is the part of password.Hasher the account routes use.
type hasher interface {
	Hash(ctx context.Context, pw string) (string, error)
	Verify(ctx context.Context, pw, phc string) (bool, error)
	VerifyDummy(ctx context.Context, pw string) error
}

type userJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type sessionJSON struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      userJSON  `json:"user"`
}

type sessionInfoJSON struct {
	User      userJSON          `json:"user"`
	Kind      model.SessionKind `json:"kind"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type identityRequest struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
}

type passwordRequest struct {
	Password string `json:"password"`
}

type accountRoute struct {
	allow string
	serve func(*handler, http.ResponseWriter, *http.Request)
}

var accountRoutes = map[string]accountRoute{
	"auth":             {http.MethodGet, (*handler).authMode},
	"sessions/local":   {http.MethodPost, (*handler).sessionsLocal},
	"sessions/oidc":    {http.MethodPost, (*handler).sessionsOIDC},
	"bootstrap/import": {http.MethodPost, (*handler).bootstrapImport},
	"session":          {http.MethodGet + ", " + http.MethodDelete, (*handler).currentSession},
	"sessions/reauth":  {http.MethodPost, (*handler).reauth},
	"identities":       {http.MethodPost, (*handler).identities},
}

// account serves rest if it is an account route and reports whether it was one.
func (h *handler) account(w http.ResponseWriter, r *http.Request, rest string) bool {
	route, ok := accountRoutes[rest]
	if !ok {
		return false
	}
	if !slices.Contains(strings.Split(route.allow, ", "), r.Method) {
		w.Header().Set("Allow", route.allow)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return true
	}
	route.serve(h, w, r)
	return true
}

func refuse(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func refuseField(w http.ResponseWriter, msg, field string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg, "field": field})
}

func decodeAccount(w http.ResponseWriter, r *http.Request, dst any) bool {
	if status, code, field := decodeStrict(r, constants.AccountMaxBodyBytes, dst); status != 0 {
		writeDecodeFailure(w, status, code, field)
		return false
	}
	return true
}

// hashErr turns a hashing call whose context ended into errUnavailable (503).
func hashErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errUnavailable, err)
	}
	return err
}

func toUserJSON(u store.User) userJSON { return userJSON{ID: u.ID, Username: u.Username, Role: u.Role} }

// identityField names the invalid field of an identity, or is empty.
func identityField(iss, sub string) string {
	u, err := url.Parse(iss)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "iss"
	}
	if len(sub) == 0 || len(sub) > constants.IdentitySubjectMaxBytes {
		return "sub"
	}
	return ""
}

func (h *handler) authMode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]config.AuthMode{"mode": h.cfg.AuthMode})
}

func (h *handler) sessionsLocal(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.AuthMode.Allows(model.KindLocal) {
		refuse(w, http.StatusForbidden, errMode)
		return
	}
	var req credentialsRequest
	if !decodeAccount(w, r, &req) {
		return
	}
	u, hash, err := h.st.UserByUsername(r.Context(), req.Username)
	if errors.Is(err, store.ErrNotFound) {
		if err := h.hs.VerifyDummy(r.Context(), req.Password); err != nil {
			h.fail(w, r, hashErr(err))
			return
		}
		refuse(w, http.StatusUnauthorized, errCredentials)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok, err := h.hs.Verify(r.Context(), req.Password, hash)
	if err != nil {
		h.fail(w, r, hashErr(err))
		return
	}
	if !ok || u.Disabled {
		refuse(w, http.StatusUnauthorized, errCredentials)
		return
	}
	h.createSession(w, r, u, model.KindLocal)
}

func (h *handler) sessionsOIDC(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.AuthMode.Allows(model.KindOIDC) {
		refuse(w, http.StatusForbidden, errMode)
		return
	}
	var req identityRequest
	if !decodeAccount(w, r, &req) {
		return
	}
	if f := identityField(req.Iss, req.Sub); f != "" {
		refuseField(w, errIdentity, f)
		return
	}
	u, err := h.st.UserByIdentity(r.Context(), req.Iss, req.Sub)
	if errors.Is(err, store.ErrNotFound) || err == nil && u.Disabled {
		refuse(w, http.StatusForbidden, errNotLinked)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.createSession(w, r, u, model.KindOIDC)
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request, u store.User, kind model.SessionKind) {
	b := make([]byte, constants.SessionTokenBytes)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	exp, err := h.st.CreateSession(r.Context(), h.clk.Now(), u.ID, kind, sha256.Sum256([]byte(tok)))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sessionJSON{Token: tok, ExpiresAt: exp, User: toUserJSON(u)})
}

// bootstrapImport validates before it answers skipped, and hashes only while no user exists.
func (h *handler) bootstrapImport(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if !decodeAccount(w, r, &req) {
		return
	}
	if !store.ValidUsername(req.Username) {
		refuseField(w, "invalid_username", "username")
		return
	}
	if !password.Valid(req.Password) {
		refuseField(w, "invalid_password", "password")
		return
	}
	n, err := h.st.CountUsers(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	result := "skipped"
	if n == 0 {
		hash, err := h.hs.Hash(r.Context(), req.Password)
		if err != nil {
			h.fail(w, r, hashErr(err))
			return
		}
		_, err = h.st.CreateFirstUser(r.Context(), h.clk.Now(), req.Username, hash)
		switch {
		case err == nil:
			result = "imported"
		case !errors.Is(err, store.ErrUsersExist):
			h.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

func (h *handler) currentSession(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if tok := r.Header.Get(HeaderSession); tok != "" {
			if err := h.st.DeleteSession(r.Context(), sha256.Sum256([]byte(tok))); err != nil {
				h.fail(w, r, err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ses, _, err := h.session(r)
	if err != nil {
		h.sessionFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionInfoJSON{User: toUserJSON(ses.User), Kind: ses.Kind, ExpiresAt: ses.ExpiresAt})
}

// linkable applies the session, mode and session_kind checks of re-auth and link, in that order.
func (h *handler) linkable(w http.ResponseWriter, r *http.Request) (store.Session, [32]byte, bool) {
	ses, sum, err := h.session(r)
	switch {
	case err != nil:
		h.sessionFail(w, r, err)
	case h.cfg.AuthMode != config.AuthBoth:
		refuse(w, http.StatusForbidden, errMode)
	case ses.Kind != model.KindLocal:
		refuse(w, http.StatusForbidden, errSessionKind)
	default:
		return ses, sum, true
	}
	return ses, sum, false
}

func (h *handler) reauth(w http.ResponseWriter, r *http.Request) {
	ses, sum, ok := h.linkable(w, r)
	if !ok {
		return
	}
	var req passwordRequest
	if !decodeAccount(w, r, &req) {
		return
	}
	u, hash, err := h.st.UserByUsername(r.Context(), ses.User.Username)
	if errors.Is(err, store.ErrNotFound) || err == nil && u.Disabled {
		refuse(w, http.StatusUnauthorized, errSession)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok, err = h.hs.Verify(r.Context(), req.Password, hash)
	if err != nil {
		h.fail(w, r, hashErr(err))
		return
	}
	if !ok {
		if _, err := h.st.RecordReauthFailure(r.Context(), sum); err != nil && !errors.Is(err, store.ErrNotFound) {
			h.fail(w, r, err)
			return
		}
		refuse(w, http.StatusUnauthorized, errCredentials)
		return
	}
	err = h.st.MarkReauth(r.Context(), h.clk.Now(), sum)
	if errors.Is(err, store.ErrNotFound) {
		refuse(w, http.StatusUnauthorized, errSession)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) identities(w http.ResponseWriter, r *http.Request) {
	ses, _, ok := h.linkable(w, r)
	if !ok {
		return
	}
	var req identityRequest
	if !decodeAccount(w, r, &req) {
		return
	}
	if f := identityField(req.Iss, req.Sub); f != "" {
		refuseField(w, errIdentity, f)
		return
	}
	now := h.clk.Now()
	if ses.ReauthAt == nil || now.After(ses.ReauthAt.Add(constants.ReauthWindow)) {
		refuse(w, http.StatusForbidden, errReauth)
		return
	}
	err := h.st.Link(r.Context(), now, ses.User.ID, req.Iss, req.Sub)
	switch {
	case errors.Is(err, store.ErrAlreadyLinked):
		refuse(w, http.StatusConflict, errAlreadyLinked)
	case errors.Is(err, store.ErrNotFound):
		refuse(w, http.StatusUnauthorized, errSession)
	case err != nil:
		h.fail(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
