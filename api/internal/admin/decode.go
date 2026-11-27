package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/davcs86/commsgun/api/internal/enqueue"
)

// decodeStrict answers status 0 on success; 413 means an empty body, 422 carries code and field.
func decodeStrict(r *http.Request, max int64, dst any) (status int, code, field string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return http.StatusUnprocessableEntity, "invalid_json", ""
	}
	if int64(len(raw)) > max {
		return http.StatusRequestEntityTooLarge, "", ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var te *json.UnmarshalTypeError
		if name, ok := enqueue.UnknownField(err); ok {
			return http.StatusUnprocessableEntity, "unknown_field", name
		}
		if errors.As(err, &te) {
			return http.StatusUnprocessableEntity, "invalid_json", te.Field
		}
		return http.StatusUnprocessableEntity, "invalid_json", ""
	}
	if dec.More() {
		return http.StatusUnprocessableEntity, "invalid_json", ""
	}
	return 0, "", ""
}

// writeDecodeFailure renders a decodeStrict refusal.
func writeDecodeFailure(w http.ResponseWriter, status int, code, field string) {
	if status == http.StatusRequestEntityTooLarge {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]string{"error": code, "field": field})
}
