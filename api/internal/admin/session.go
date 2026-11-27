package admin

import (
	"crypto/sha256"
	"errors"
	"net/http"

	"github.com/davcs86/commsgun/api/internal/store"
)

// HeaderSession carries the session token; a query string never does.
const HeaderSession = "Commsgun-Session"

// session resolves HeaderSession; a session whose kind the mode forbids is deleted and refused.
func (h *handler) session(r *http.Request) (store.Session, [32]byte, bool) {
	tok := r.Header.Get(HeaderSession)
	if tok == "" {
		return store.Session{}, [32]byte{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	ses, err := h.st.SessionByHash(r.Context(), h.clk.Now(), sum)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.log.Error("session lookup failed", "error", err.Error())
		}
		return store.Session{}, sum, false
	}
	if !h.cfg.AuthMode.Allows(ses.Kind) {
		if err := h.st.DeleteSession(r.Context(), sum); err != nil {
			h.log.Error("session delete failed", "error", err.Error())
		}
		return store.Session{}, sum, false
	}
	return ses, sum, true
}
