package indexer

import (
	"context"
	"log/slog"

	"github.com/Ani-tem/sift/internal/embed"
	"github.com/Ani-tem/sift/internal/store"
)

// Embedder is the part of embed.Client the embed pass needs; tests fake it.
type Embedder interface {
	Documents(ctx context.Context, texts []string) ([][]float32, error)
}

const embedBatch = 32

// Embed gives every chunk without a vector one. It runs after extraction as
// its own pass, so keyword search works even when Ollama is off, and since
// progress is stored per chunk, stopping and re-running loses nothing.
func Embed(ctx context.Context, st *store.Store, e Embedder, log *slog.Logger,
	progress func(done, total int)) (done, failed int, err error) {
	total, err := st.CountPending(ctx)
	if err != nil {
		return 0, 0, err
	}
	var after int64
	for {
		batch, err := st.PendingChunks(ctx, after, embedBatch)
		if err != nil || len(batch) == 0 {
			return done, failed, err
		}
		after = batch[len(batch)-1].ID

		ids, vecs, err := embedAll(ctx, e, batch)
		if err != nil {
			// One bad chunk must not sink the other 31 (the same poison
			// batch problem the hirewire audit found in its notifier), so
			// retry one by one and skip only what still fails. `after`
			// moves past it, so this run doesn't loop on it forever.
			if ctx.Err() != nil {
				return done, failed, ctx.Err()
			}
			ids, vecs = nil, nil
			for _, c := range batch {
				i, v, err := embedAll(ctx, e, []store.PendingChunk{c})
				if err != nil {
					log.Debug("embed", "chunk", c.ID, "err", err)
					failed++
					continue
				}
				ids, vecs = append(ids, i...), append(vecs, v...)
			}
		}
		if err := st.SaveVectors(ctx, ids, vecs); err != nil {
			return done, failed, err
		}
		done += len(ids)
		if progress != nil {
			progress(done, total)
		}
	}
}

func embedAll(ctx context.Context, e Embedder, batch []store.PendingChunk) ([]int64, [][]byte, error) {
	texts := make([]string, len(batch))
	ids := make([]int64, len(batch))
	for i, c := range batch {
		texts[i], ids[i] = c.Text, c.ID
	}
	raw, err := e.Documents(ctx, texts)
	if err != nil {
		return nil, nil, err
	}
	vecs := make([][]byte, len(raw))
	for i, v := range raw {
		vecs[i] = embed.Quantize(v)
	}
	return ids, vecs, nil
}
