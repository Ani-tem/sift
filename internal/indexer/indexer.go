// Package indexer brings the index up to date with the disk.
//
// One walker stats every file and sends only new or changed ones to a pool
// of extract workers (slow, CPU-bound: PDF parsing and OCR). A single
// writer batches their results into SQLite, which allows one writer at a time.
//
//	walk ──jobs──▶ N extract workers ──docs──▶ writer ──▶ SQLite
package indexer

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Ani-tem/sift/internal/extract"
	"github.com/Ani-tem/sift/internal/store"
)

const (
	MaxFileSize = 50 << 20 // bigger files are videos, disk images, datasets
	ChunkSize   = 1000
	batchSize   = 64
)

// skipDirs are never worth searching: dependencies, caches, app bundles.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "__pycache__": true, "venv": true,
	"Library": true, "target": true, "DerivedData": true, "Pods": true,
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || skipDirs[name] ||
		strings.HasSuffix(name, ".app") || strings.HasSuffix(name, ".photoslibrary")
}

type Indexer struct {
	Store   *store.Store
	Extract *extract.Extractor
	Workers int
	Log     *slog.Logger
	// Progress, if set, is called after each batch is written.
	Progress func(Stats)
}

type Stats struct {
	Seen, Unchanged, Indexed, Failed, Removed int
}

type job struct {
	path string
	stat store.Stat
	kind extract.Kind
}

func (ix *Indexer) Run(ctx context.Context, roots []string) (Stats, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var st Stats
	known, err := ix.Store.Known(ctx)
	if err != nil {
		return st, err
	}

	jobs := make(chan job)
	docs := make(chan store.Doc)
	// Owned by the walker goroutine; read only after it exits.
	seen := make(map[string]bool)
	var walked Stats

	var walkers sync.WaitGroup
	walkers.Go(func() {
		defer close(jobs)
		for _, root := range roots {
			ix.walk(ctx, root, known, seen, jobs, &walked)
		}
	})

	var workers sync.WaitGroup
	for range ix.Workers {
		workers.Go(func() {
			for j := range jobs {
				docs <- ix.extract(ctx, j)
			}
		})
	}
	go func() { workers.Wait(); close(docs) }()

	// Writer: this goroutine. Flush every batchSize docs or every second,
	// whichever comes first, so progress is visible and one commit covers
	// many files (a commit is an fsync; 10k of them would be slow).
	var batch []store.Doc
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ix.Store.Save(ctx, batch, time.Now().UnixNano()); err != nil {
			return err
		}
		for _, d := range batch {
			st.Indexed++
			if d.Err != "" {
				st.Failed++
			}
		}
		batch = batch[:0]
		if ix.Progress != nil {
			ix.Progress(st)
		}
		return nil
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for docs != nil {
		select {
		case d, ok := <-docs:
			if !ok {
				docs = nil
				break
			}
			if batch = append(batch, d); len(batch) >= batchSize {
				err = flush()
			}
		case <-tick.C:
			err = flush()
		}
		if err != nil {
			// Stop the walk and drain so every goroutine can exit.
			cancel()
			go func() {
				for range docs {
				}
			}()
			return st, err
		}
	}
	if err := flush(); err != nil {
		return st, err
	}
	walkers.Wait()
	st.Seen, st.Unchanged = walked.Seen, walked.Unchanged
	if ctx.Err() != nil {
		// Interrupted: the walk is incomplete, so "not seen" doesn't mean
		// "deleted". Keep everything; the next run finishes the job.
		return st, ctx.Err()
	}

	var gone []string
	for p := range known {
		if !seen[p] && under(p, roots) {
			gone = append(gone, p)
		}
	}
	if err := ix.Store.Delete(ctx, gone); err != nil {
		return st, err
	}
	st.Removed = len(gone)
	return st, nil
}

func (ix *Indexer) walk(ctx context.Context, root string, known map[string]store.Stat,
	seen map[string]bool, jobs chan<- job, st *Stats) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil {
			// Permission denied and friends: skip it, keep walking.
			ix.Log.Debug("walk", "path", path, "err", err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		kind, ok := extract.KindOf(path)
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 || info.Size() > MaxFileSize {
			return nil
		}
		// The stat is taken *before* the file is read. If the file changes
		// while we extract it, the stored mtime is the old one, so the next
		// run sees a difference and re-indexes. We never store new text
		// under an old stat or vice versa, so we can't wrongly skip it later.
		stat := store.Stat{Size: info.Size(), Mtime: info.ModTime().UnixNano()}
		seen[path] = true
		st.Seen++
		if known[path] == stat {
			st.Unchanged++
			return nil
		}
		select {
		case jobs <- job{path: path, stat: stat, kind: kind}:
		case <-ctx.Done():
			return fs.SkipAll
		}
		return nil
	})
}

func (ix *Indexer) extract(ctx context.Context, j job) store.Doc {
	d := store.Doc{Path: j.path, Stat: j.stat, Kind: string(j.kind)}
	text, err := ix.Extract.Extract(ctx, j.path, j.kind)
	if err != nil {
		ix.Log.Debug("extract", "path", j.path, "err", err)
		d.Err = err.Error()
		return d
	}
	d.Chunks = extract.Chunk(text, ChunkSize)
	return d
}

func under(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
