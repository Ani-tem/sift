package store

import (
	"context"
	"slices"
	"strings"

	"github.com/Ani-tem/sift/internal/embed"
)

type PendingChunk struct {
	ID   int64
	Text string
}

// PendingChunks returns up to n chunks after id `after` that have no
// embedding yet. Progress is just "which rows exist", so an interrupted
// embed pass resumes where it stopped.
func (s *Store) PendingChunks(ctx context.Context, after int64, n int) ([]PendingChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, trim(c.name || ' ' || c.text) FROM chunks c
		WHERE c.id > ? AND NOT EXISTS (SELECT 1 FROM vectors v WHERE v.chunk_id = c.id)
		ORDER BY c.id LIMIT ?`, after, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingChunk
	for rows.Next() {
		var c PendingChunk
		if err := rows.Scan(&c.ID, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM chunks c
		WHERE NOT EXISTS (SELECT 1 FROM vectors v WHERE v.chunk_id = c.id)`).Scan(&n)
	return n, err
}

func (s *Store) SaveVectors(ctx context.Context, ids []int64, vecs [][]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		// The chunk may have been deleted (its file re-indexed) while we were
		// embedding it. Then the SELECT finds no row and nothing is stored.
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO vectors (chunk_id, embedding)
			SELECT id, ? FROM chunks WHERE id = ?`, vecs[i], id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type scored struct {
	chunk int64
	score int32
}

// Nearest returns the files whose chunks are closest in meaning to q, best
// first, one hit per file. It compares q with every stored vector (brute
// force): exact, and simple enough to be the baseline that a smarter index
// (HNSW, step 5) must beat.
func (s *Store) Nearest(ctx context.Context, q []byte, limit int) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chunk_id, embedding FROM vectors`)
	if err != nil {
		return nil, err
	}
	var all []scored
	for rows.Next() {
		var id int64
		var v []byte
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return nil, err
		}
		if len(v) == len(q) {
			all = append(all, scored{id, embed.Dot(q, v)})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(all, func(a, b scored) int { return int(b.score - a.score) })

	seen := make(map[string]bool)
	var hits []Hit
	for _, c := range all {
		if len(hits) == limit {
			break
		}
		var h Hit
		var name, text string
		err := s.db.QueryRowContext(ctx, `
			SELECT f.path, f.kind, c.name, c.text FROM chunks c JOIN files f ON f.id = c.file_id
			WHERE c.id = ?`, c.chunk).Scan(&h.Path, &h.Kind, &name, &text)
		if err != nil {
			continue // deleted since the scan
		}
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		h.Score = float64(c.score) / (127 * 127) // back to cosine, ~[-1, 1]
		h.Snippet = excerpt(text, name)
		hits = append(hits, h)
	}
	return hits, nil
}

func excerpt(text, name string) string {
	if text == "" {
		return name
	}
	if len(text) <= 160 {
		return text
	}
	cut := strings.LastIndexByte(text[:160], ' ')
	if cut <= 0 {
		cut = 160
	}
	return text[:cut] + "…"
}

// Hybrid merges keyword and meaning-based results with Reciprocal Rank
// Fusion: each list votes 1/(k+rank) for its files. BM25 scores and cosine
// similarities aren't on the same scale, so we can't just add them, but
// ranks are always comparable. k=60 is the value from the original RRF
// paper; it stops the #1 result of one list from drowning out everything.
func (s *Store) Hybrid(ctx context.Context, query string, q []byte, limit int) ([]Hit, error) {
	const k = 60
	kw, err := s.Search(ctx, query, limit*3)
	if err != nil {
		return nil, err
	}
	vec, err := s.Nearest(ctx, q, limit*3)
	if err != nil {
		return nil, err
	}
	score := make(map[string]float64)
	best := make(map[string]Hit)
	for _, list := range [][]Hit{kw, vec} { // keyword first: its snippets highlight words
		for rank, h := range list {
			score[h.Path] += 1.0 / float64(k+rank+1)
			if _, ok := best[h.Path]; !ok {
				best[h.Path] = h
			}
		}
	}
	var out []Hit
	for p, h := range best {
		h.Score = score[p]
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b Hit) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out[:min(limit, len(out))], nil
}
