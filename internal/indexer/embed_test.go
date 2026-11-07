package indexer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ani-tem/sift/internal/embed"
	"github.com/Ani-tem/sift/internal/store"
)

// fakeModel embeds by topic: words about pay share one direction, words
// about cats another. That's the property a real model gives us.
type fakeModel struct{ calls int }

var topics = [][]string{
	{"salary", "compensation", "ctc", "pay"},
	{"cat", "kitten", "meow"},
	{"raft", "leader", "consensus"},
}

func vec(text string) []float32 {
	v := make([]float32, len(topics)+1)
	v[len(topics)] = 0.1 // so no vector is all zeros
	for i, words := range topics {
		for _, w := range words {
			if strings.Contains(strings.ToLower(text), w) {
				v[i] = 1
			}
		}
	}
	return v
}

func (f *fakeModel) Documents(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if strings.Contains(t, "POISON") {
			return nil, errors.New("model rejected input")
		}
		out[i] = vec(t)
	}
	return out, nil
}

func TestEmbedThenHybridFindsByMeaning(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Save(ctx, []store.Doc{
		{Path: "/d/offer.pdf", Kind: "pdf", Chunks: []string{"Your total compensation will be 18 LPA"}},
		{Path: "/d/notes.md", Kind: "text", Chunks: []string{"raft leader election notes"}},
		{Path: "/d/bad.txt", Kind: "text", Chunks: []string{"POISON chunk"}},
		{Path: "/d/meme.png", Kind: "image", Chunks: []string{"meow when the build passes"}},
	}, 1)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &fakeModel{}
	done, failed, err := Embed(ctx, st, m, log, nil)
	if err != nil || done != 3 || failed != 1 {
		t.Fatalf("done=%d failed=%d err=%v; want 3 done, 1 poison skipped", done, failed, err)
	}

	// "salary" appears in no file, so keyword search alone finds nothing...
	if hits, _ := st.Search(ctx, "salary", 5); len(hits) != 0 {
		t.Fatalf("keyword search unexpectedly found %v", hits)
	}
	// ...but by meaning it's the offer letter.
	hits, err := st.Hybrid(ctx, "salary", embed.Quantize(vec("salary")), 5)
	if err != nil || len(hits) == 0 || hits[0].Path != "/d/offer.pdf" {
		t.Fatalf("hybrid salary: %v %v", hits, err)
	}
	// Exact words still win through the keyword half.
	hits, _ = st.Hybrid(ctx, "build passes", embed.Quantize(vec("build passes")), 5)
	if len(hits) == 0 || hits[0].Path != "/d/meme.png" {
		t.Fatalf("hybrid keyword: %v", hits)
	}

	// Re-running embeds nothing new (the poison chunk is retried and fails again).
	m.calls = 0
	if done, _, _ := Embed(ctx, st, m, log, nil); done != 0 {
		t.Fatalf("second pass embedded %d", done)
	}

	// Re-indexing a file drops its old vectors with its old chunks.
	st.Save(ctx, []store.Doc{{Path: "/d/offer.pdf", Kind: "pdf", Chunks: []string{"rejected, sorry"}}}, 2)
	s, _ := st.Stats(ctx)
	if s.Vectors != 2 {
		t.Fatalf("vectors = %d, want 2 after re-index", s.Vectors)
	}
}
