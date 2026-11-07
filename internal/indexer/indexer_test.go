package indexer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ani-tem/sift/internal/extract"
	"github.com/Ani-tem/sift/internal/store"
)

func TestRunIsIncremental(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("notes/raft.md", "leader election")
	write("todo.txt", "buy milk")
	write("node_modules/x/readme.md", "ignored dependency")
	write(".secret/key.txt", "hidden dir")
	write("song.mp3", "not a document")

	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ix := &Indexer{Store: st, Extract: &extract.Extractor{Timeout: time.Second}, Workers: 3,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	s, err := ix.Run(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if s.Seen != 2 || s.Indexed != 2 {
		t.Fatalf("first run: %+v, want 2 seen and indexed", s)
	}

	// Nothing changed: nothing is re-read.
	if s, _ = ix.Run(ctx, []string{root}); s.Indexed != 0 || s.Unchanged != 2 {
		t.Fatalf("second run: %+v", s)
	}

	// One edit, one delete.
	write("todo.txt", "buy oat milk and eggs") // size changes, so it's re-read
	os.Remove(filepath.Join(root, "notes/raft.md"))
	if s, _ = ix.Run(ctx, []string{root}); s.Indexed != 1 || s.Removed != 1 {
		t.Fatalf("third run: %+v", s)
	}
	if hits, _ := st.Search(ctx, "eggs", 5); len(hits) != 1 {
		t.Fatal("edit not picked up")
	}
	if hits, _ := st.Search(ctx, "election", 5); len(hits) != 0 {
		t.Fatal("deleted file still found")
	}
}
