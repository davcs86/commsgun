package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/model"
)

const (
	probeApp       = "ops"
	probeSubject   = "commsgun probe"
	probeText      = "Hello world"
	probeKeyPrefix = "probe:"
	probeMethods   = "GET, POST"
	headerIdemKey  = "Idempotency-Key"
	// unknownFieldPrefix is encoding/json's DisallowUnknownFields error text before the quoted name.
	unknownFieldPrefix = "json: unknown field "
)

var probeChannels = []model.Channel{model.ChannelEmail, model.ChannelSMS}

type probeChannel struct {
	Channel  model.Channel `json:"channel"`
	Enabled  bool          `json:"enabled"`
	Provider *string       `json:"provider"`
	Sender   *string       `json:"sender"`
}

type probeRequest struct {
	Channel  string          `json:"channel"`
	To       string          `json:"to"`
	Provider json.RawMessage `json:"provider"`
}

// probeMessage uses the enqueue contract's field names; it is the body Submit validates and hashes.
type probeMessage struct {
	Channel string   `json:"channel"`
	To      []string `json:"to"`
	Subject string   `json:"subject,omitempty"`
	Text    string   `json:"text,omitempty"`
	Body    string   `json:"body,omitempty"`
}

func (h *handler) probes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.probeOptions(w)
	case http.MethodPost:
		status, channel, requestID := h.probeSend(w, r)
		h.log.Info("probe", "status", status, "channel", channel, "request_id", requestID)
	default:
		w.Header().Set("Allow", probeMethods)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (h *handler) providerName(ch model.Channel) *string {
	if p := h.cfg.Providers[ch]; p != nil {
		return &p.Name
	}
	return nil
}

func (h *handler) probeOptions(w http.ResponseWriter) {
	app, available := h.cfg.Apps[probeApp]
	channels := []probeChannel{}
	if available {
		for _, ch := range probeChannels {
			cc, enabled := app.Channels[ch]
			pc := probeChannel{Channel: ch, Enabled: enabled, Provider: h.providerName(ch)}
			if cc.DefaultSender != nil {
				pc.Sender = &cc.DefaultSender.Raw
			}
			channels = append(channels, pc)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": probeApp, "available": available, "channels": channels})
}

// probeSend returns only what may be logged: the status, the channel name and the request id.
func (h *handler) probeSend(w http.ResponseWriter, r *http.Request) (status int, channel, requestID string) {
	reject := func(code int, msg, field string) (int, string, string) {
		body := map[string]string{"error": msg}
		if code == http.StatusUnprocessableEntity {
			body["field"] = field
		}
		writeJSON(w, code, body)
		return code, channel, ""
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, constants.ProbeMaxBodyBytes+1))
	if err != nil {
		return reject(http.StatusUnprocessableEntity, "invalid_json", "")
	}
	if len(raw) > constants.ProbeMaxBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return http.StatusRequestEntityTooLarge, "", ""
	}
	var req probeRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var te *json.UnmarshalTypeError
		if quoted, ok := strings.CutPrefix(err.Error(), unknownFieldPrefix); ok {
			name, _ := strconv.Unquote(quoted)
			return reject(http.StatusUnprocessableEntity, "unknown_field", name)
		}
		if errors.As(err, &te) {
			return reject(http.StatusUnprocessableEntity, "invalid_json", te.Field)
		}
		return reject(http.StatusUnprocessableEntity, "invalid_json", "")
	}
	if dec.More() {
		return reject(http.StatusUnprocessableEntity, "invalid_json", "")
	}
	ch := model.Channel(req.Channel)
	if ch != model.ChannelEmail && ch != model.ChannelSMS {
		return reject(http.StatusUnprocessableEntity, "invalid_channel", "channel")
	}
	channel = req.Channel

	app, ok := h.cfg.Apps[probeApp]
	if !ok {
		return reject(http.StatusConflict, "probe_app_missing", "")
	}
	if len(req.Provider) == 0 {
		return reject(http.StatusUnprocessableEntity, "provider_required", "provider")
	}
	var got *string
	if json.Unmarshal(req.Provider, &got) != nil {
		return reject(http.StatusUnprocessableEntity, "invalid_json", "provider")
	}
	if want := h.providerName(ch); (got == nil) != (want == nil) || (got != nil && *got != *want) {
		return reject(http.StatusConflict, "provider_mismatch", "")
	}
	key := r.Header.Get(headerIdemKey)
	if key == "" {
		return reject(http.StatusUnprocessableEntity, "idempotency_key_required", headerIdemKey)
	}

	msg := probeMessage{Channel: req.Channel, To: []string{req.To}}
	if ch == model.ChannelEmail {
		msg.Subject, msg.Text = probeSubject, probeText
	} else {
		msg.Body = probeText
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return reject(http.StatusInternalServerError, "internal", "")
	}
	out, fail := enqueue.Submit(r.Context(), h.cfg, h.st, h.clk.Now(), &app, probeKeyPrefix+key, body, enqueue.SourceIP(r))
	if fail != nil {
		enqueue.WriteFailure(w, fail)
		return fail.Status, channel, ""
	}
	status = http.StatusAccepted
	if out.Result.Duplicate {
		status = http.StatusOK
	}
	ids := out.Result.MessageIDs
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, status, map[string]any{
		"request_id": out.Result.RequestID, "message_ids": ids, "duplicate": out.Result.Duplicate, "held": out.Held,
	})
	return status, channel, out.Result.RequestID
}
