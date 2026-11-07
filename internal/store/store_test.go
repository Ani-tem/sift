package store

import (
	"context"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func paths(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out
}

func TestSearchFindsBodyNameAndStems(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	err := s.Save(ctx, []Doc{
		{Path: "/d/offer.pdf", Kind: "pdf", Chunks: []string{"We are pleased to offer you the role. Compensation: 18 LPA."}},
		{Path: "/d/Anirudh_resume_cisco.pdf", Kind: "pdf", Chunks: []string{"Go, PostgreSQL, Docker"}},
		{Path: "/d/notes.md", Kind: "text", Chunks: []string{"raft elections and leaders"}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		"compensation":    "/d/offer.pdf",
		"resume cisco":    "/d/Anirudh_resume_cisco.pdf", // file name
		"raft election":   "/d/notes.md",                 // stemming: elections
		"postgr":          "/d/Anirudh_resume_cisco.pdf", // prefix on last word
		`"go" -- docker:`: "/d/Anirudh_resume_cisco.pdf", // syntax chars are harmless
	} {
		hits, err := s.Search(ctx, q, 10)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(hits) == 0 || hits[0].Path != want {
			t.Errorf("%q: got %v, want %s first", q, paths(hits), want)
		}
	}
}

func TestSearchFallsBackToAnyWord(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.Save(ctx, []Doc{{Path: "/d/a.txt", Kind: "text", Chunks: []string{"kafka consumer groups"}}}, 1)
	hits, _ := s.Search(ctx, "kafka zookeeper", 10)
	if len(hits) != 1 {
		t.Fatalf("got %v, want the kafka file via OR fallback", paths(hits))
	}
}

func TestSaveReplacesAndDeleteForgets(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.Save(ctx, []Doc{{Path: "/d/a.txt", Kind: "text", Chunks: []string{"old words"}}}, 1)
	s.Save(ctx, []Doc{{Path: "/d/a.txt", Stat: Stat{Size: 9, Mtime: 2}, Kind: "text", Chunks: []string{"new words"}}}, 2)

	if hits, _ := s.Search(ctx, "old", 10); len(hits) != 0 {
		t.Fatal("stale text still searchable after re-save")
	}
	if hits, _ := s.Search(ctx, "new", 10); len(hits) != 1 {
		t.Fatal("new text not searchable")
	}
	if k, _ := s.Known(ctx); k["/d/a.txt"] != (Stat{Size: 9, Mtime: 2}) {
		t.Fatalf("stat not updated: %+v", k)
	}
	if err := s.Delete(ctx, []string{"/d/a.txt"}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Stats(ctx)
	if st.Files != 0 || st.Chunks != 0 {
		t.Fatalf("delete left %+v", st)
	}
	if hits, _ := s.Search(ctx, "new", 10); len(hits) != 0 {
		t.Fatal("deleted file still searchable")
	}
}

func TestSearchOneHitPerFile(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.Save(ctx, []Doc{{Path: "/d/book.pdf", Kind: "pdf", Chunks: []string{"golang one", "golang two", "golang three"}}}, 1)
	if hits, _ := s.Search(ctx, "golang", 10); len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
}
