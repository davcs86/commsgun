package store

import (
	"context"
	"database/sql"
	"errors"
)

// LookupRequest returns the stored hash and original result for an idempotency key, if any.
func (s *Store) LookupRequest(ctx context.Context, app, idempotencyKey string) (hash [32]byte, res EnqueueResult, found bool, err error) {
	var id string
	var h []byte
	err = s.r.QueryRowContext(ctx,
		`SELECT id, body_hash FROM requests WHERE app=? AND idempotency_key=?`,
		app, idempotencyKey).Scan(&id, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return hash, res, false, nil
	}
	if err != nil {
		return hash, res, false, translate(err)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT id FROM messages WHERE request_id=? ORDER BY rowid`, id)
	if err != nil {
		return hash, res, false, translate(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return hash, res, false, translate(err)
		}
		ids = append(ids, m)
	}
	if err := rows.Err(); err != nil {
		return hash, res, false, translate(err)
	}
	copy(hash[:], h)
	return hash, EnqueueResult{RequestID: id, MessageIDs: ids, Duplicate: true}, true, nil
}
