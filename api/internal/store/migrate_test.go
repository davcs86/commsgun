package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func objects(t *testing.T, db *sql.DB, names ...string) int {
	t.Helper()
	q := `SELECT COUNT(*) FROM sqlite_master`
	args := make([]any, len(names))
	if len(names) > 0 {
		q += ` WHERE name IN (?` + strings.Repeat(",?", len(names)-1) + `)`
		for i, n := range names {
			args[i] = n
		}
	}
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func TestFreshDatabaseReachesLatest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := version(t, s.w); v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
	if n := objects(t, s.w, "users", "identities", "sessions", "sessions_user"); n != 4 {
		t.Fatalf("account objects = %d, want 4", n)
	}
}

func TestMigratesTodaysSchema(t *testing.T) {
	dir := t.TempDir()
	db := rawDB(t, filepath.Join(dir, dbFile))
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO requests(id, app, idempotency_key, body_hash, created_at) VALUES ('r1','app1','k1',x'00',1);
INSERT INTO messages(id, request_id, app, channel, recipient, sender, state, created_at, updated_at, expires_at)
VALUES ('m1','r1','app1','email','r@x.com','a@x.com','queued',1,1,2);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := version(t, s.w); v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
	var rcpt string
	if err := s.w.QueryRow(`SELECT recipient FROM messages WHERE id='m1'`).Scan(&rcpt); err != nil || rcpt != "r@x.com" {
		t.Fatalf("row = %q (%v)", rcpt, err)
	}
}

func TestMigrationsApplyOnceInOrder(t *testing.T) {
	db := rawDB(t, filepath.Join(t.TempDir(), "x.db"))
	files := mapFS(map[string]string{
		"0003_alter.sql":  `ALTER TABLE a ADD COLUMN y;`,
		"0002_create.sql": `CREATE TABLE a (x);`,
	})
	if err := migrate(db, schemaSQL, files); err != nil {
		t.Fatal(err)
	}
	if v := version(t, db); v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
	if _, err := db.Exec(`INSERT INTO a(x, y) VALUES (1, 2)`); err != nil {
		t.Fatalf("column from 0003 missing: %v", err)
	}
	if err := migrate(db, schemaSQL, files); err != nil {
		t.Fatalf("second run reapplied a migration: %v", err)
	}
	if v := version(t, db); v != 3 {
		t.Fatalf("version after rerun = %d, want 3", v)
	}
}

func TestNewerDatabaseRefused(t *testing.T) {
	db := rawDB(t, filepath.Join(t.TempDir(), "x.db"))
	if _, err := db.Exec(schemaSQL + `UPDATE schema_version SET version=3;`); err != nil {
		t.Fatal(err)
	}
	before := objects(t, db)
	err := migrate(db, schemaSQL, mapFS(map[string]string{"0002_create.sql": `CREATE TABLE a (x);`}))
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err = %v, want one containing \"newer\"", err)
	}
	if n := objects(t, db); n != before {
		t.Fatalf("objects = %d, want %d", n, before)
	}
	if v := version(t, db); v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	db := rawDB(t, filepath.Join(t.TempDir(), "x.db"))
	bad := mapFS(map[string]string{"0002_bad.sql": "CREATE TABLE a (x);\nINSERT INTO missing_table VALUES (1);"})
	if err := migrate(db, schemaSQL, bad); err == nil {
		t.Fatal("expected failure")
	}
	if v := version(t, db); v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}
	if n := objects(t, db, "a"); n != 0 {
		t.Fatal("partial table a left behind")
	}
}

func TestMigrationGapRefused(t *testing.T) {
	db := rawDB(t, filepath.Join(t.TempDir(), "x.db"))
	files := mapFS(map[string]string{"0002_a.sql": `CREATE TABLE a (x);`, "0004_b.sql": `CREATE TABLE b (x);`})
	err := migrate(db, schemaSQL, files)
	if err == nil || !strings.Contains(err.Error(), "0003") {
		t.Fatalf("err = %v, want one naming 0003", err)
	}
	if n := objects(t, db); n != 0 {
		t.Fatalf("objects = %d, want 0", n)
	}
}
