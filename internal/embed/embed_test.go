package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuantizedDotIsCosine(t *testing.T) {
	a := Quantize([]float32{3, 4})   // direction (0.6, 0.8)
	b := Quantize([]float32{30, 40}) // same direction, 10x longer
	c := Quantize([]float32{-4, 3})  // perpendicular
	if got := float64(Dot(a, b)) / (127 * 127); got < 0.99 {
		t.Fatalf("same direction: cosine %.3f, want ~1", got)
	}
	if got := Dot(a, c); got < -200 || got > 200 {
		t.Fatalf("perpendicular: dot %d, want ~0", got)
	}
}

func TestClientSendsPrefixes(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			return
		}
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req.Input...)
		out := make([][]float32, len(req.Input))
		for i := range out {
			out[i] = []float32{1, 0}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	defer srv.Close()

	c := New()
	c.URL = srv.URL
	ctx := context.Background()
	if !c.Available(ctx) {
		t.Fatal("server not detected")
	}
	if _, err := c.Documents(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, "q"); err != nil {
		t.Fatal(err)
	}
	want := "search_document: a|search_document: b|search_query: q"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q", got)
	}
}
