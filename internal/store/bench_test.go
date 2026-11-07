package store

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"testing"
)

// BenchmarkNearest scans 60k vectors, about the size of a real home folder.
func BenchmarkNearest(b *testing.B) {
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "index.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	const n = 60_000
	docs := make([]Doc, n/100)
	for i := range docs {
		docs[i] = Doc{Path: string(rune('a'+i%26)) + string(rune(i)), Kind: "text", Chunks: make([]string, 100)}
	}
	s.Save(ctx, docs, 1)
	rnd := func() []byte {
		v := make([]byte, 768)
		for i := range v {
			v[i] = byte(rand.IntN(255) - 127)
		}
		return v
	}
	ids := make([]int64, n)
	vecs := make([][]byte, n)
	for i := range ids {
		ids[i], vecs[i] = int64(i+1), rnd()
	}
	if err := s.SaveVectors(ctx, ids, vecs); err != nil {
		b.Fatal(err)
	}
	q := rnd()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Nearest(ctx, q, 30); err != nil {
			b.Fatal(err)
		}
	}
}
