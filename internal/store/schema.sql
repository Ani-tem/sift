CREATE TABLE IF NOT EXISTS files (
    id         INTEGER PRIMARY KEY,
    path       TEXT    NOT NULL UNIQUE,
    size       INTEGER NOT NULL,
    mtime      INTEGER NOT NULL,      -- unix ns, with size: "did it change?"
    kind       TEXT    NOT NULL,      -- text | pdf | image
    indexed_at INTEGER NOT NULL,
    err        TEXT                   -- set when extraction failed
);

-- Chunk text lives in a normal table; chunks_fts indexes it without storing
-- a second copy ("external content"). Triggers keep the two in step.
CREATE TABLE IF NOT EXISTS chunks (
    id      INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files (id),
    seq     INTEGER NOT NULL,
    name    TEXT    NOT NULL,         -- file name words, first chunk only
    text    TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS chunks_file ON chunks (file_id);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5 (
    name, text,
    content = 'chunks', content_rowid = 'id',
    tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts (rowid, name, text) VALUES (new.id, new.name, new.text);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, name, text) VALUES ('delete', old.id, old.name, old.text);
END;

-- One embedding per chunk: 768 int8 values (see embed.Quantize).
CREATE TABLE IF NOT EXISTS vectors (
    chunk_id  INTEGER PRIMARY KEY REFERENCES chunks (id),
    embedding BLOB NOT NULL
);
CREATE TRIGGER IF NOT EXISTS chunks_ad_vectors AFTER DELETE ON chunks BEGIN
    DELETE FROM vectors WHERE chunk_id = old.id;
END;
