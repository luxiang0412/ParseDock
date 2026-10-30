package coordinator

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"parsedock.dev/parsedock/migrations"
)

// Open opens the SQLite database in WAL mode with conservative pragmas.
//
// The database file must live on local disk owned by this single Coordinator
// process; WAL is explicitly unsupported on network filesystems.
func Open(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("_pragma", "")
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single writer connection avoids SQLITE_BUSY storms; readers are cheap.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return db, nil
}

// Migrate applies embedded migrations idempotently.
func Migrate(db *sql.DB) error {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = ?`, "mig:"+e.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		b, err := migrations.FS.ReadFile(e.Name())
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(b)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)`,
			"mig:"+e.Name(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ts formats a time for storage.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// tsPtr formats an optional time.
func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

// parseTS parses a stored timestamp.
func parseTS(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func parseTSPtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := parseTS(ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// nowUTC returns the current time truncated to microseconds so that values
// round-trip identically through SQLite text storage.
func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
