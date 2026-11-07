# sift: AI search for your Mac

Search everything on this Mac by what it *says*, not what it's called:

    sift "offer letter I signed"
    sift "screenshot of the go race detector error"
    sift "resume with the cisco project"

Everything runs locally. No cloud, no API keys, ₹0.

## Goals

- Index Desktop, Documents and Downloads: text and code files, PDFs, and the text inside screenshots and photos.
- Answer queries in under 100 ms from a warm index.
- Stay incremental. The first index is slow; after that, only changed files are re-read.
- Meaning-based search: "salary" should find a PDF that says "compensation".
- Never slow the Mac down. Indexing runs in the background at low priority.

Non-goals for now: indexing mail and messages, sync across devices, Windows or Linux.

## Architecture

```
            ┌────────────── sift (Go) ──────────────┐
 files ──▶  scanner ──▶ extract workers (N) ──▶ writer ──▶ SQLite
            (walk +      text: read directly     (one       files
             mtime/size  pdf/ocr: sift-extract   goroutine, chunks (FTS5)
             diff)       (Swift helper)          batched)   vectors (step 3)
            └───────────────────────────────────────┘
                                   ▲
 sift "query" ─────────────────────┘ keyword (BM25) + vector → fused ranking
```

**Why these pieces:**

- **Go service + Swift helper.** macOS already ships PDF text extraction (PDFKit) and very good OCR (Vision), for free and on-device. Go can't call those easily, so a 60-line Swift CLI (`sift-extract`) does it and prints the text. Go owns everything else.
- **SQLite, not Postgres.** It's a local app: one file, no server. SQLite's FTS5 gives a real inverted index with BM25 ranking. We use the pure-Go driver (`modernc.org/sqlite`), so there's no cgo.
- **Many extractors, one writer.** Extraction (OCR especially) is slow and CPU-bound, so it runs in a worker pool. SQLite allows one writer at a time, so a single goroutine owns all writes and batches them into transactions. That's the same split as hirewire's dispatcher and workers, but the bottleneck is reversed.
- **Change detection by (size, mtime).** It's cheap: one `stat` per file, no reads. Files whose size and mtime haven't changed are skipped. Rows for files that disappeared are deleted at the end of a scan.

## Data model

```sql
files(id, path UNIQUE, size, mtime, kind, indexed_at, err)
chunks  -- FTS5(name, text, file_id UNINDEXED), porter stemming
vectors(chunk_id, embedding BLOB)          -- step 3
```

A file is split into chunks of about 1,000 characters, so a hit points to the relevant part of a long PDF and embeddings stay within the model's input size.

## Search

1. **Keyword (step 1):** FTS5 MATCH with BM25, best chunk per file, snippet with the hit highlighted. User input is tokenised and quoted, so `"`, `-` and `:` can't break the FTS query syntax. The last word gets prefix matching (`resu` → `resume`).
2. **Semantic (step 3):** embed every chunk with a local model (Ollama `nomic-embed-text`, about 270 MB, runs on the M2). Embed the query and find the nearest chunks by cosine similarity.
3. **Hybrid (step 4):** merge the two rankings with Reciprocal Rank Fusion, `score = Σ 1/(60 + rank)`. Keyword search wins on exact names and error strings; vectors win on paraphrases. RRF needs no score calibration between the two.

## Build order

1. Scanner, text/PDF/OCR extraction, SQLite FTS index, `sift index` and `sift search`. **(this step)**
2. Watch mode: FSEvents, so new files are searchable within seconds; low-priority background agent (launchd).
3. Embeddings with Ollama; vectors stored in SQLite; brute-force cosine first, with benchmarks.
4. Hybrid ranking with RRF, plus an eval set of 30 real queries → measure recall@5 before and after.
5. An HNSW vector index written from scratch, benchmarked against brute force (recall versus latency).
6. Menu bar app with a global hotkey; open or reveal results in Finder.

## Interview angles

- Incremental indexing and consistency: what happens when a file changes mid-extraction?
- Single-writer design and batching under SQLite's locking model.
- Inverted index versus vector index; why hybrid search beats either alone, *measured*.
- HNSW: graph construction, the recall/latency trade-off, memory cost.
