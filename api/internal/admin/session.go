package admin

import (
	"crypto/sha256"
	"errors"
	"net/http"

	"github.com/davcs86/commsgun/api/internal/store"
)

// HeaderSession carries the session token; a query string never does.
const HeaderSession = "Commsgun-Session"

// errNoSession is a missing, unknown, expired or mode-refused session: 401 session, never a store fault.
var errNoSession = errors.New("admin: no session")

// session resolves HeaderSession; a session whose kind the mode forbids is deleted and refused.
func (h *handler) session(r *http.Request) (store.Session, [32]byte, error) {
	tok := r.Header.Get(HeaderSession)
	if tok == "" {
		return store.Session{}, [32]byte{}, errNoSession
	}
	sum := sha256.Sum256([]byte(tok))
	ses, err := h.st.SessionByHash(r.Context(), h.clk.Now(), sum)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, sum, errNoSession
	}
	if err != nil {
		return store.Session{}, sum, err
	}
	if !h.cfg.AuthMode.Allows(ses.Kind) {
		if err := h.st.DeleteSession(r.Context(), sum); err != nil {
			h.log.Error("session delete failed", "error", err.Error())
		}
		return store.Session{}, sum, errNoSession
	}
	return ses, sum, nil
}

// sessionFail answers 401 session for errNoSession and h.fail for a store fault.
func (h *handler) sessionFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNoSession) {
		refuse(w, http.StatusUnauthorized, errSession)
		return
	}
	h.fail(w, r, err)
}
