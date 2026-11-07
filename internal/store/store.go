// Package store keeps the index in one SQLite file: a row per file and an
// FTS5 full-text index over the file's text chunks.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// WAL lets `sift search` read while `sift index` writes in another process.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: this process has a single writer goroutine anyway, and
	// SQLite serialises writers, so a pool would only add SQLITE_BUSY retries.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Stat is what we compare to decide whether a file changed since last index.
type Stat struct {
	Size  int64
	Mtime int64 // unix nanoseconds
}

// Known returns the recorded stat of every indexed file.
func (s *Store) Known(ctx context.Context) (map[string]Stat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, size, mtime FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]Stat)
	for rows.Next() {
		var p string
		var st Stat
		if err := rows.Scan(&p, &st.Size, &st.Mtime); err != nil {
			return nil, err
		}
		m[p] = st
	}
	return m, rows.Err()
}

// Doc is one extracted file, ready to store.
type Doc struct {
	Path   string
	Stat   Stat
	Kind   string
	Chunks []string
	Err    string // extraction failed; the file stays findable by name
}

// Save replaces the stored version of each doc, all in one transaction.
func (s *Store) Save(ctx context.Context, docs []Doc, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range docs {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO files (path, size, mtime, kind, indexed_at, err)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''))
			ON CONFLICT (path) DO UPDATE SET
				size = excluded.size, mtime = excluded.mtime, kind = excluded.kind,
				indexed_at = excluded.indexed_at, err = excluded.err
			RETURNING id`,
			d.Path, d.Stat.Size, d.Stat.Mtime, d.Kind, now, d.Err).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_id = ?`, id); err != nil {
			return err
		}
		// The file name goes only on the first chunk: it's searchable, but a
		// 200-chunk PDF doesn't get 200 votes for its name.
		name := nameWords(d.Path)
		chunks := d.Chunks
		if len(chunks) == 0 {
			chunks = []string{""}
		}
		for i, c := range chunks {
			if i == 1 {
				name = ""
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO chunks (file_id, seq, name, text) VALUES (?, ?, ?, ?)`,
				id, i, name, c); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Delete forgets files that no longer exist.
func (s *Store) Delete(ctx context.Context, paths []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range paths {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chunks WHERE file_id = (SELECT id FROM files WHERE path = ?)`, p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Hit struct {
	Path    string
	Kind    string
	Snippet string  // matched words wrapped in Mark ... Unmark
	Score   float64 // BM25, lower is better
}

const (
	Mark   = "\x02"
	Unmark = "\x03"
)

// Search returns the best-matching files, one hit per file. It first asks
// for every query word; if nothing matches, it accepts any word.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	hits, err := s.match(ctx, strings.Join(words, " "), limit)
	if err != nil || len(hits) > 0 || len(words) == 1 {
		return hits, err
	}
	return s.match(ctx, strings.Join(words, " OR "), limit)
}

func (s *Store) match(ctx context.Context, fts string, limit int) ([]Hit, error) {
	// Name matches weigh 4x a body match. Ask for extra rows because one
	// file can own several of the top chunks.
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.path, f.kind,
		       snippet(chunks_fts, -1, ?, ?, '…', 14),
		       bm25(chunks_fts, 4.0, 1.0) AS score
		FROM chunks_fts
		JOIN chunks c ON c.id = chunks_fts.rowid
		JOIN files f ON f.id = c.file_id
		WHERE chunks_fts MATCH ?
		ORDER BY score
		LIMIT ?`, Mark, Unmark, fts, limit*8)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]bool)
	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.Kind, &h.Snippet, &h.Score); err != nil {
			return nil, err
		}
		if seen[h.Path] || len(hits) == limit {
			continue
		}
		seen[h.Path] = true
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// queryWords turns free text into safe FTS5 terms. Each word is quoted, so
// characters like " - : * can't be read as query syntax, and the last word
// gets a prefix match so results show up while you're still typing.
func queryWords(q string) []string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	if n := len(words); n > 0 {
		words[n-1] += "*"
	}
	return words
}

// nameWords makes a file name searchable: "Anirudh_resume-cisco.pdf"
// becomes "Anirudh resume cisco pdf".
func nameWords(path string) string {
	return strings.Join(strings.FieldsFunc(filepath.Base(path), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}), " ")
}

type Stats struct {
	Files, Chunks, Vectors, Failed int
	ByKind                         map[string]int
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	st := Stats{ByKind: map[string]int{}}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), count(err) FROM files`).Scan(&st.Files, &st.Failed); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM chunks), (SELECT count(*) FROM vectors)`).Scan(&st.Chunks, &st.Vectors); err != nil {
		return st, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, count(*) FROM files GROUP BY kind`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		st.ByKind[k] = n
	}
	return st, rows.Err()
}
