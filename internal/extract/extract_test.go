package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChunk(t *testing.T) {
	text := strings.Repeat("word ", 500) // 2500 bytes
	chunks := Chunk(strings.TrimSpace(text), 1000)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 1000 || strings.HasPrefix(c, " ") || strings.HasSuffix(c, "rd w") {
			t.Fatalf("bad chunk boundary: %q...", c[:20])
		}
	}
	if got := strings.Join(chunks, " "); got != strings.TrimSpace(text) {
		t.Fatal("chunks lost text")
	}
}

func TestChunkLongWordKeepsRunesWhole(t *testing.T) {
	text := strings.Repeat("é", 800) // 1600 bytes, no spaces
	for _, c := range Chunk(text, 1001) {
		if !strings.HasPrefix(c, "é") || strings.ContainsRune(c, '�') {
			t.Fatal("rune split across chunks")
		}
	}
}

func TestExtractTextSkipsBinary(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "notes.md")
	bad := filepath.Join(dir, "blob.json")
	os.WriteFile(good, []byte("# Raft\n\nleader   election\n"), 0o600)
	os.WriteFile(bad, []byte("{\x00\x01}"), 0o600)

	e := &Extractor{}
	got, err := e.Extract(context.Background(), good, Text)
	if err != nil || got != "# Raft leader election" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := e.Extract(context.Background(), bad, Text); err == nil {
		t.Fatal("binary file was read as text")
	}
}
