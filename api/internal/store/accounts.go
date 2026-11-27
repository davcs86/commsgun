package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/google/uuid"
	"modernc.org/sqlite"
)

const (
	roleAdmin              = "admin"
	sqliteConstraintPK     = 1555
	sqliteConstraintUnique = 2067
)

var (
	ErrUsersExist    = errors.New("store: users already exist")
	ErrUserExists    = errors.New("store: username taken")
	ErrAlreadyLinked = errors.New("store: identity already linked")
)

type User struct {
	ID, Username, Role string
	Disabled           bool
}

type Session struct {
	User      User
	Kind      model.SessionKind
	ExpiresAt time.Time
	ReauthAt  *time.Time
}

// ValidUsername reports whether s is UsernameMinLen..UsernameMaxLen of a-z0-9._-.
func ValidUsername(s string) bool {
	if len(s) < constants.UsernameMinLen || len(s) > constants.UsernameMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func isUnique(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && (se.Code() == sqliteConstraintUnique || se.Code() == sqliteConstraintPK)
}

// CreateFirstUser adds an admin only while users is empty, else ErrUsersExist.
func (s *Store) CreateFirstUser(ctx context.Context, now time.Time, username, hash string) (User, error) {
	return s.insertUser(ctx, now, username, hash, true)
}

// AddUser adds an admin; a taken username, even a disabled one, is ErrUserExists.
func (s *Store) AddUser(ctx context.Context, now time.Time, username, hash string) (User, error) {
	return s.insertUser(ctx, now, username, hash, false)
}

func (s *Store) insertUser(ctx context.Context, now time.Time, username, hash string, onlyFirst bool) (u User, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return u, translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()
	if onlyFirst {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return u, err
		}
		if n > 0 {
			return u, ErrUsersExist
		}
	}
	u = User{ID: uuid.NewString(), Username: username, Role: roleAdmin}
	ms := now.UTC().UnixMilli()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO users(id, username, role, password_hash, disabled, created_at, updated_at) VALUES (?,?,?,?,0,?,?)`,
		u.ID, username, roleAdmin, hash, ms, ms); err != nil {
		if isUnique(err) {
			return User{}, ErrUserExists
		}
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, translate(err)
}

// UserByUsername returns the user, disabled or not, and its password hash, or ErrNotFound.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, string, error) {
	var u User
	var hash string
	err := s.r.QueryRowContext(ctx,
		`SELECT id, username, role, disabled, password_hash FROM users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	if err != nil {
		return User{}, "", translate(err)
	}
	return u, hash, nil
}

// UserByIdentity returns the linked user, disabled or not, or ErrNotFound.
func (s *Store) UserByIdentity(ctx context.Context, iss, sub string) (User, error) {
	var u User
	err := s.r.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.role, u.disabled FROM identities i JOIN users u ON u.id = i.user_id
		 WHERE i.issuer=? AND i.subject=?`, iss, sub).
		Scan(&u.ID, &u.Username, &u.Role, &u.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, translate(err)
	}
	return u, nil
}

// SetPassword replaces the hash and deletes the user's sessions.
func (s *Store) SetPassword(ctx context.Context, now time.Time, username, hash string) error {
	_, err := s.userWrite(ctx, username, true, false,
		`UPDATE users SET password_hash=?, updated_at=? WHERE username=? RETURNING id`, hash, now.UTC().UnixMilli(), username)
	return err
}

// SetDisabled flips disabled; disabling deletes the user's sessions.
func (s *Store) SetDisabled(ctx context.Context, now time.Time, username string, disabled bool) error {
	_, err := s.userWrite(ctx, username, disabled, false,
		`UPDATE users SET disabled=?, updated_at=? WHERE username=? RETURNING id`, disabled, now.UTC().UnixMilli(), username)
	return err
}

// Unlink removes every identity of the user and its sessions, returning how many identities went.
func (s *Store) Unlink(ctx context.Context, now time.Time, username string) (int, error) {
	return s.userWrite(ctx, username, true, true,
		`UPDATE users SET updated_at=? WHERE username=? RETURNING id`, now.UTC().UnixMilli(), username)
}

// userWrite runs update (which must return the user's id; none is ErrNotFound) and the deletes in one transaction.
func (s *Store) userWrite(ctx context.Context, username string, endSessions, unlink bool, update string, args ...any) (removed int, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()
	var id string
	if err = tx.QueryRowContext(ctx, update, args...).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if endSessions {
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id); err != nil {
			return 0, err
		}
	}
	if unlink {
		res, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE user_id=?`, id)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed = int(n)
	}
	return removed, tx.Commit()
}

// Link records (iss, sub) for userID; a taken pair or a second identity for iss is ErrAlreadyLinked.
func (s *Store) Link(ctx context.Context, now time.Time, userID, iss, sub string) error {
	res, err := s.w.ExecContext(ctx,
		`INSERT INTO identities(user_id, issuer, subject, created_at) SELECT id, ?, ?, ? FROM users WHERE id=?`,
		iss, sub, now.UTC().UnixMilli(), userID)
	if isUnique(err) {
		return ErrAlreadyLinked
	}
	return affectedFound(res, err)
}

// CreateSession stores tokenHash for userID and returns its expiry, now+SessionTTL.
func (s *Store) CreateSession(ctx context.Context, now time.Time, userID string, kind model.SessionKind, tokenHash [32]byte) (time.Time, error) {
	nowMS := now.UTC().UnixMilli()
	expMS := now.UTC().Add(constants.SessionTTL).UnixMilli()
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO sessions(token_hash, user_id, kind, created_at, expires_at) VALUES (?,?,?,?,?)`,
		tokenHash[:], userID, kind, nowMS, expMS); err != nil {
		return time.Time{}, translate(err)
	}
	return time.UnixMilli(expMS).UTC(), nil
}

// SessionByHash returns a live session of an enabled user, else ErrNotFound; expired rows are refused, not swept.
func (s *Store) SessionByHash(ctx context.Context, now time.Time, tokenHash [32]byte) (Session, error) {
	var ses Session
	var expMS int64
	var reauthMS sql.NullInt64
	err := s.r.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.role, u.disabled, s.kind, s.expires_at, s.reauth_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash=? AND s.expires_at>? AND u.disabled=0`,
		tokenHash[:], now.UTC().UnixMilli()).
		Scan(&ses.User.ID, &ses.User.Username, &ses.User.Role, &ses.User.Disabled, &ses.Kind, &expMS, &reauthMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, translate(err)
	}
	ses.ExpiresAt = time.UnixMilli(expMS).UTC()
	if reauthMS.Valid {
		t := time.UnixMilli(reauthMS.Int64).UTC()
		ses.ReauthAt = &t
	}
	return ses, nil
}

// DeleteSession removes the session; an absent one is not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash [32]byte) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash[:])
	return translate(err)
}

// MarkReauth sets reauth_at=now on a live session, else ErrNotFound.
func (s *Store) MarkReauth(ctx context.Context, now time.Time, tokenHash [32]byte) error {
	nowMS := now.UTC().UnixMilli()
	res, err := s.w.ExecContext(ctx,
		`UPDATE sessions SET reauth_at=? WHERE token_hash=? AND expires_at>?`, nowMS, tokenHash[:], nowMS)
	return affectedFound(res, err)
}

// RecordReauthFailure counts a failure (never reset); the ReauthMaxFailures-th deletes the session.
func (s *Store) RecordReauthFailure(ctx context.Context, tokenHash [32]byte) (deleted bool, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return false, translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()
	var n int
	if err = tx.QueryRowContext(ctx,
		`UPDATE sessions SET reauth_failures=reauth_failures+1 WHERE token_hash=? RETURNING reauth_failures`,
		tokenHash[:]).Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if n >= constants.ReauthMaxFailures {
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash[:]); err != nil {
			return false, err
		}
		deleted = true
	}
	return deleted, tx.Commit()
}

func affectedFound(res sql.Result, err error) error {
	if err != nil {
		return translate(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return translate(err)
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
