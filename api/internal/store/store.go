// Package store is the SQLite persistence layer: one writer connection, a read-only pool.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/davcs86/commsgun/api/internal/constants"
	"modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const (
	dbFile        = "commsgun.db"
	schemaVersion = 1
	sqliteBusy    = 5
	sqliteFull    = 13
)

var ErrBusy = errors.New("store: database busy or full")

type Store struct {
	w, r    *sql.DB
	cleanup string
}

// Open opens (creating if needed) dataDir/commsgun.db.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, dbFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create db file: %w", err)
	}
	f.Close()
	// auto_vacuum must precede journal_mode so it applies before any page is written.
	wdsn := fmt.Sprintf("file:%s?_txlock=immediate&_pragma=auto_vacuum(INCREMENTAL)&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path, constants.SQLiteBusyTimeout.Milliseconds())
	w, err := sql.Open("sqlite", wdsn)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := initSchema(w, schemaSQL); err != nil {
		w.Close()
		return nil, err
	}
	rdsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)", path, constants.SQLiteBusyTimeout.Milliseconds())
	r, err := sql.Open("sqlite", rdsn)
	if err != nil {
		w.Close()
		return nil, err
	}
	return &Store{w: w, r: r}, nil
}

// OpenMemory returns a throwaway store backed by a temp dir removed on Close.
func OpenMemory() (*Store, error) {
	dir, err := os.MkdirTemp("", "commsgun-store-")
	if err != nil {
		return nil, err
	}
	s, err := Open(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s.cleanup = dir
	return s, nil
}

func (s *Store) Close() error {
	err := errors.Join(s.r.Close(), s.w.Close())
	if s.cleanup != "" {
		err = errors.Join(err, os.RemoveAll(s.cleanup))
	}
	return err
}

func initSchema(w *sql.DB, ddl string) error {
	tx, err := w.Begin()
	if err != nil {
		return translate(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='schema_version'`).Scan(&n); err != nil {
		return translate(err)
	}
	if n == 0 {
		if _, err := tx.Exec(ddl); err != nil {
			return translate(err)
		}
	}
	var v int
	if err := tx.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil {
		return translate(err)
	}
	if v != schemaVersion {
		return fmt.Errorf("store: schema version %d, want %d", v, schemaVersion)
	}
	return translate(tx.Commit())
}

// translate maps lock/full conditions to ErrBusy and passes other errors through.
func translate(err error) error {
	if err == nil || errors.Is(err, ErrBusy) {
		return err
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqliteBusy, sqliteFull:
			return fmt.Errorf("%w: %w", ErrBusy, err)
		}
	}
	if strings.Contains(err.Error(), "database is locked") {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}
