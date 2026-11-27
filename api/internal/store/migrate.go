package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationName = regexp.MustCompile(`^([0-9]{4})_[^/]+\.sql$`)

type migration struct {
	n    int
	name string
}

// migrate applies base (version 1) to an empty database, then each NNNN_<name>.sql above schema_version.
func migrate(w *sql.DB, base string, migrations fs.FS) error {
	list, err := migrationList(migrations)
	if err != nil {
		return err
	}
	latest := 1
	if len(list) > 0 {
		latest = list[len(list)-1].n
	}
	v, err := migrateBase(w, base, latest)
	if err != nil {
		return err
	}
	for _, m := range list {
		if m.n <= v {
			continue
		}
		ddl, err := fs.ReadFile(migrations, m.name)
		if err != nil {
			return fmt.Errorf("store: read migration %04d: %w", m.n, err)
		}
		if err := applyMigration(w, m.n, string(ddl)); err != nil {
			return fmt.Errorf("store: migration %04d: %w", m.n, err)
		}
	}
	return nil
}

// migrationList returns the files numbered 0002 upward with no gap or duplicate.
func migrationList(migrations fs.FS) ([]migration, error) {
	names, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return nil, err
	}
	list := make([]migration, 0, len(names))
	for _, name := range names {
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("store: migration file %q is not NNNN_<name>.sql", name)
		}
		n, _ := strconv.Atoi(m[1])
		list = append(list, migration{n: n, name: name})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n < list[j].n })
	for i, m := range list {
		if want := i + 2; m.n < want {
			return nil, fmt.Errorf("store: migration %04d is a duplicate or below 0002", m.n)
		} else if m.n > want {
			return nil, fmt.Errorf("store: migration %04d missing", want)
		}
	}
	return list, nil
}

func migrateBase(w *sql.DB, base string, latest int) (v int, err error) {
	tx, err := w.Begin()
	if err != nil {
		return 0, translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()
	var n int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='schema_version'`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		if _, err = tx.Exec(base); err != nil {
			return 0, err
		}
	}
	if err = tx.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	if v > latest {
		return 0, fmt.Errorf("store: schema version %d is newer than this image's %d", v, latest)
	}
	return v, tx.Commit()
}

// applyMigration re-reads the version under the write lock: another process may have applied n already.
func applyMigration(w *sql.DB, n int, ddl string) (err error) {
	tx, err := w.Begin()
	if err != nil {
		return translate(err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			err = translate(err)
		}
	}()
	var v int
	if err = tx.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	if v >= n {
		return tx.Rollback()
	}
	if v != n-1 {
		return fmt.Errorf("store: schema version %d, want %d", v, n-1)
	}
	if _, err = tx.Exec(ddl); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE schema_version SET version=? WHERE version=?`, n, n-1)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("store: schema version moved during migration %04d", n)
	}
	return tx.Commit()
}
