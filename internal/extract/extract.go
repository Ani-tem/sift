// Package extract turns a file on disk into plain text chunks.
//
// Plain text and code are read directly. PDFs and images go through
// sift-extract, a small Swift helper that uses macOS's own PDFKit and
// Vision OCR, so there is nothing to install and nothing leaves the Mac.
package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type Kind string

const (
	Text  Kind = "text"
	PDF   Kind = "pdf"
	Image Kind = "image"
)

// MaxText caps how much text we keep from one file. A 300-page PDF is
// about 1 MB of text; anything bigger is usually a data dump, not a document.
const MaxText = 2 << 20

var kinds = map[string]Kind{
	".pdf": PDF,
	".png": Image, ".jpg": Image, ".jpeg": Image, ".heic": Image, ".webp": Image,
}

var textExts = strings.Fields(`.txt .md .markdown .rst .tex .csv .tsv .log .json .yaml .yml
	.toml .ini .xml .html .css .sql .sh .zsh .go .py .js .jsx .ts .tsx .java .kt .swift
	.c .h .cc .cpp .hpp .rs .rb .php .dart .cs .scala .lua .r .ipynb`)

func init() {
	for _, e := range textExts {
		kinds[e] = Text
	}
}

// KindOf reports how a file should be read, judged by its extension.
func KindOf(path string) (Kind, bool) {
	k, ok := kinds[strings.ToLower(filepath.Ext(path))]
	return k, ok
}

type Extractor struct {
	Helper  string        // path to the sift-extract binary
	Timeout time.Duration // per-file limit for the helper
}

// Extract returns the file's text with whitespace collapsed.
func (e *Extractor) Extract(ctx context.Context, path string, kind Kind) (string, error) {
	var raw []byte
	var err error
	switch kind {
	case Text:
		raw, err = readText(path)
	case PDF:
		raw, err = e.helper(ctx, "pdf", path)
	case Image:
		raw, err = e.helper(ctx, "ocr", path)
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
	if err != nil {
		return "", err
	}
	if len(raw) > MaxText {
		raw = raw[:MaxText]
	}
	s := strings.ToValidUTF8(string(raw), "")
	return strings.Join(strings.Fields(s), " "), nil
}

var errBinary = errors.New("looks binary")

func readText(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, MaxText)); err != nil {
		return nil, err
	}
	head := buf.Bytes()[:min(buf.Len(), 8<<10)]
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, errBinary
	}
	return buf.Bytes(), nil
}

func (e *Extractor) helper(ctx context.Context, mode, path string) ([]byte, error) {
	if e.Helper == "" {
		return nil, errors.New("sift-extract helper not found")
	}
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Helper, mode, path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Chunk splits text into pieces of at most size bytes, breaking at spaces,
// so a search hit points at the right part of a long document.
func Chunk(text string, size int) []string {
	var out []string
	for len(text) > size {
		cut := strings.LastIndexByte(text[:size], ' ')
		if cut < size/2 { // one huge "word": cut anyway, on a rune boundary
			cut = size
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}
