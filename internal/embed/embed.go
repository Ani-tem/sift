// Package embed turns text into vectors with a local Ollama model, so that
// texts with similar meaning end up close together ("salary" near
// "compensation") even when they share no words.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

const (
	DefaultURL   = "http://localhost:11434"
	DefaultModel = "nomic-embed-text"
	Dims         = 768
)

// nomic-embed-text is trained with these task prefixes; leaving them out
// measurably hurts retrieval.
const (
	docPrefix   = "search_document: "
	queryPrefix = "search_query: "
)

type Client struct {
	URL   string
	Model string
	HTTP  *http.Client
}

func New() *Client {
	return &Client{URL: DefaultURL, Model: DefaultModel, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Available reports whether Ollama is running, without waiting long.
func (c *Client) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/version", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Documents embeds chunks of indexed files.
func (c *Client) Documents(ctx context.Context, texts []string) ([][]float32, error) {
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = docPrefix + t
	}
	return c.embed(ctx, in)
}

// Query embeds a search query.
func (c *Client) Query(ctx context.Context, q string) ([]float32, error) {
	v, err := c.embed(ctx, []string{queryPrefix + q})
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

func (c *Client) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": c.Model, "input": input, "truncate": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: %s: %w", resp.Status, err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}
	if len(out.Embeddings) != len(input) {
		return nil, fmt.Errorf("ollama: got %d embeddings for %d inputs", len(out.Embeddings), len(input))
	}
	return out.Embeddings, nil
}

// Quantize normalises v to length 1 and stores each value as one signed
// byte. 768 float32s are 3 KB; as int8 they are 768 bytes, 4x less to
// read on every search, and the ranking barely changes (each value keeps
// ~0.4% precision, far finer than the gaps between relevant and not).
//
// After normalising, the dot product of two vectors is their cosine
// similarity, so search needs no division.
func Quantize(v []float32) []byte {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		norm = 1
	}
	out := make([]byte, len(v))
	for i, x := range v {
		q := math.Round(float64(x) / norm * 127)
		out[i] = byte(int8(max(-127, min(127, q))))
	}
	return out
}

// Dot is the similarity of two quantized vectors: about 127² × cosine.
func Dot(a, b []byte) int32 {
	var s int32
	b = b[:len(a)] // one bounds check here instead of one per element
	for i := range a {
		s += int32(int8(a[i])) * int32(int8(b[i]))
	}
	return s
}
