# sift

Search your Mac by what files *say*, not what they're called. It reads text files, code, PDFs and the text inside screenshots. Everything stays on your Mac: no cloud, no API keys.

```
$ sift resume cisco
~/Desktop/Resume/Anirudh_resume_cisco.pdf  pdf
   …Built and deployed a Go service that crawls 185 companies…
```

## Install

Needs Go 1.25+ and Xcode command line tools (for `swiftc`).

```
make install        # builds sift + sift-extract into ~/go/bin
sift index          # first run indexes Desktop, Documents, Downloads
sift <query>
sift stats
```

`sift index` is incremental: re-run it and only new or changed files are read.

## How it works

See [docs/design.md](docs/design.md). In short: a Go walker finds changed files using size and mtime; a worker pool extracts text, sending PDFs and images to a small Swift helper that uses macOS PDFKit and Vision OCR; one writer goroutine batches the chunks into a SQLite FTS5 index; search is ranked with BM25.

## Roadmap

- [x] 1. Incremental indexer, text/PDF/OCR extraction, FTS5 keyword search
- [ ] 2. Watch mode (FSEvents) + launchd background agent
- [ ] 3. Local embeddings (Ollama) + semantic search
- [ ] 4. Hybrid ranking (RRF) + eval set (recall@5)
- [ ] 5. HNSW vector index from scratch, benchmarked against brute force
- [ ] 6. Menu bar app + global hotkey
