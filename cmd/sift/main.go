// sift searches your Mac by what files say, not what they're called.
//
//	sift index [dir...]   index (or update) Desktop, Documents, Downloads
//	sift <query>          search
//	sift stats            what's in the index
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Ani-tem/sift/internal/extract"
	"github.com/Ani-tem/sift/internal/indexer"
	"github.com/Ani-tem/sift/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sift:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Println("usage: sift index [dir...] | sift stats | sift <query>")
		return nil
	}
	st, err := store.Open(dbPath())
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "index":
		return index(ctx, st, args[1:])
	case "stats":
		return stats(ctx, st)
	case "search":
		args = args[1:]
	}
	return search(ctx, st, strings.Join(args, " "))
}

func index(ctx context.Context, st *store.Store, roots []string) error {
	home, _ := os.UserHomeDir()
	if len(roots) == 0 {
		for _, d := range []string{"Desktop", "Documents", "Downloads"} {
			roots = append(roots, filepath.Join(home, d))
		}
	}
	for i, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return err
		}
		roots[i] = abs
	}
	// Be a polite background job: lowest CPU priority, so OCR never makes
	// the Mac feel slow. The Swift helper inherits it.
	syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19)

	level := slog.LevelInfo
	if os.Getenv("SIFT_DEBUG") != "" {
		level = slog.LevelDebug
	}
	ix := &indexer.Indexer{
		Store:   st,
		Extract: &extract.Extractor{Helper: helperPath(), Timeout: time.Minute},
		Workers: max(2, runtime.NumCPU()/2),
		Log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
		Progress: func(s indexer.Stats) {
			fmt.Fprintf(os.Stderr, "\rindexed %d files (%d failed)", s.Indexed, s.Failed)
		},
	}
	start := time.Now()
	s, err := ix.Run(ctx, roots)
	fmt.Fprintf(os.Stderr, "\r%d files seen: %d indexed, %d unchanged, %d failed, %d removed in %s\n",
		s.Seen, s.Indexed, s.Unchanged, s.Failed, s.Removed, time.Since(start).Round(time.Millisecond))
	return err
}

func search(ctx context.Context, st *store.Store, q string) error {
	start := time.Now()
	hits, err := st.Search(ctx, q, 10)
	if err != nil {
		return err
	}
	took := time.Since(start)
	if len(hits) == 0 {
		fmt.Println("no matches. Run `sift index` if you haven't yet.")
		return nil
	}
	home, _ := os.UserHomeDir()
	for _, h := range hits {
		p := h.Path
		if rel, ok := strings.CutPrefix(p, home); ok {
			p = "~" + rel
		}
		snip := strings.NewReplacer(store.Mark, "\x1b[1;33m", store.Unmark, "\x1b[0m").Replace(h.Snippet)
		fmt.Printf("\x1b[1m%s\x1b[0m  \x1b[2m%s\x1b[0m\n   %s\n\n", p, h.Kind, snip)
	}
	fmt.Fprintf(os.Stderr, "%d results in %s\n", len(hits), took.Round(time.Microsecond))
	return nil
}

func stats(ctx context.Context, st *store.Store) error {
	s, err := st.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%d files, %d chunks, %d failed\n", s.Files, s.Chunks, s.Failed)
	for k, n := range s.ByKind {
		fmt.Printf("  %-6s %d\n", k, n)
	}
	fmt.Println("index:", dbPath())
	return nil
}

func dbPath() string {
	if p := os.Getenv("SIFT_DB"); p != "" {
		return p
	}
	dir, _ := os.UserConfigDir() // ~/Library/Application Support on macOS
	return filepath.Join(dir, "sift", "index.db")
}

// helperPath finds sift-extract next to the sift binary.
func helperPath() string {
	if p := os.Getenv("SIFT_HELPER"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(exe), "sift-extract")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}
