// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the Readeck REST client.
//
// Every test stands up an httptest.Server that mimics Readeck just
// closely enough to exercise our client: it records the request
// (method, path, headers, body) and serves a canned response. Tests
// assert against both directions — that the client sends what we
// expect, and that its parsing of the response is correct.
//
// This keeps the tests hermetic (no network, no live Readeck
// instance) while still covering the real network code path.

package readeck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestServer spins up an httptest.Server with the given handler
// and returns a Client wired to it plus the server's URL so tests
// can introspect what was requested.
func newTestServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "test-token", WithHTTPClient(srv.Client()))
	return c, srv, log
}

// requestLog captures every request the test server receives so we
// can assert on method, path, headers, and body. Safe for the
// httptest server to call from inside request handlers because we
// use a real sync.Mutex.
type requestLog struct {
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   string
}

func (l *requestLog) record(r *http.Request) {
	// Read the body once, then replace r.Body with a fresh reader so
	// the test handler can decode the same payload afterwards.
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   string(body),
	})
}

func (l *requestLog) last(t *testing.T) recordedRequest {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.requests) == 0 {
		t.Fatalf("no requests recorded")
	}
	return l.requests[len(l.requests)-1]
}

// --- Client construction -----------------------------------------------

func TestNew_StripsTrailingSlash(t *testing.T) {
	c := New("https://readeck.example.com/", "tok")
	if c.baseURL != "https://readeck.example.com" {
		t.Errorf("baseURL: got %q, want %q", c.baseURL, "https://readeck.example.com")
	}
}

func TestNew_PreservesPath(t *testing.T) {
	c := New("https://readeck.example.com/api", "tok")
	if c.baseURL != "https://readeck.example.com/api" {
		t.Errorf("baseURL: got %q, want %q", c.baseURL, "https://readeck.example.com/api")
	}
}

// --- Auth ---------------------------------------------------------------

func TestClient_SendsBearerAuth(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	})

	if _, err := c.ListBookmarks(context.Background(), BookmarkListParams{}); err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}

	req := log.last(t)
	got := req.Header.Get("Authorization")
	if got != "Bearer test-token" {
		t.Errorf("Authorization: got %q, want %q", got, "Bearer test-token")
	}
}

func TestClient_SendsUserAgent(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.ListBookmarks(context.Background(), BookmarkListParams{}); err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	req := log.last(t)
	if ua := req.Header.Get("User-Agent"); ua == "" {
		t.Errorf("User-Agent header missing")
	}
}

// --- ListBookmarks ------------------------------------------------------

func TestListBookmarks_ReturnsParsedItems(t *testing.T) {
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bookmarks" {
			t.Errorf("path: got %q, want %q", r.URL.Path, "/api/bookmarks")
		}
		_, _ = w.Write([]byte(`[
			{"id":"abc","title":"Hello","loaded":true,"url":"https://example.com","labels":["news"]},
			{"id":"def","title":"World","loaded":true,"url":"https://example.org","labels":[]}
		]`))
	})

	got, err := c.ListBookmarks(context.Background(), BookmarkListParams{})
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	if got[0].ID != "abc" || got[0].Title != "Hello" {
		t.Errorf("got[0]: %+v", got[0])
	}
	if got[1].ID != "def" || got[1].Title != "World" {
		t.Errorf("got[1]: %+v", got[1])
	}
}

func TestListBookmarks_SendsFiltersAsQueryParams(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("limit"); got != "25" {
			t.Errorf("limit: got %q, want %q", got, "25")
		}
		if got := q.Get("has_labels"); got != "false" {
			t.Errorf("has_labels: got %q, want %q", got, "false")
		}
		if got := q.Get("sort"); got != "created" {
			t.Errorf("sort: got %q, want %q", got, "created")
		}
		_, _ = w.Write([]byte(`[]`))
	})

	_, err := c.ListBookmarks(context.Background(), BookmarkListParams{
		Limit:     25,
		HasLabels: ptrBool(false),
		Sort:      "created",
	})
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	_ = log.last(t)
}

func TestListBookmarks_OmitsUnsetFilters(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Has("limit") {
			t.Errorf("limit should not be sent when zero, got %q", q.Get("limit"))
		}
		if q.Has("has_labels") {
			t.Errorf("has_labels should not be sent when nil")
		}
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.ListBookmarks(context.Background(), BookmarkListParams{}); err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	_ = log.last(t)
}

func TestListBookmarks_PropagatesHTTPError(t *testing.T) {
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	_, err := c.ListBookmarks(context.Background(), BookmarkListParams{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestListBookmarks_FollowsNextLink(t *testing.T) {
	var page int
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		switch page {
		case 1:
			w.Header().Set("Link", `</api/bookmarks?page=2&limit=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"id":"a"},{"id":"b"}]`))
		case 2:
			_, _ = w.Write([]byte(`[{"id":"c"},{"id":"d"}]`))
		default:
			t.Errorf("unexpected page request #%d", page)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	got, err := c.ListBookmarksAll(context.Background(), BookmarkListParams{})
	if err != nil {
		t.Fatalf("ListBookmarksAll: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("len: got %d, want 4 (got %+v)", len(got), got)
	}
}

// --- GetBookmark --------------------------------------------------------

func TestGetBookmark_ReturnsDetail(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bookmarks/abc123" {
			t.Errorf("path: got %q, want %q", r.URL.Path, "/api/bookmarks/abc123")
		}
		_, _ = w.Write([]byte(`{
			"id":"abc123",
			"title":"My Bookmark",
			"url":"https://example.com/post",
			"loaded":true,
			"type":"article",
			"labels":["tech","go"]
		}`))
	})

	got, err := c.GetBookmark(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetBookmark: %v", err)
	}
	if got.ID != "abc123" {
		t.Errorf("ID: got %q", got.ID)
	}
	if got.Type != "article" {
		t.Errorf("Type: got %q", got.Type)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "tech" {
		t.Errorf("Labels: got %v", got.Labels)
	}
	_ = log.last(t)
}

func TestGetBookmark_404ReturnsNotFound(t *testing.T) {
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	})
	_, err := c.GetBookmark(context.Background(), "missing")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error should be ErrNotFound, got: %v", err)
	}
}

// --- GetArticleMarkdown -------------------------------------------------

func TestGetArticleMarkdown_ReturnsBody(t *testing.T) {
	const body = "# Hello\n\nThis is the article body."
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bookmarks/abc/article.md" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(body))
	})

	got, err := c.GetArticleMarkdown(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetArticleMarkdown: %v", err)
	}
	if got != body {
		t.Errorf("body: got %q, want %q", got, body)
	}
	_ = log.last(t)
}

func TestGetArticleMarkdown_404WhenNoArticle(t *testing.T) {
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.GetArticleMarkdown(context.Background(), "noarticle")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// --- UpdateBookmarkLabels ----------------------------------------------

func TestUpdateBookmarkLabels_SendsAddAndRemove(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method: got %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/api/bookmarks/abc" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: got %q, want application/json", ct)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		add, ok := got["add_labels"].([]any)
		if !ok {
			t.Fatalf("add_labels missing or wrong type: %+v", got)
		}
		if len(add) != 2 {
			t.Errorf("add_labels len: got %d, want 2", len(add))
		}
		// readeck accepts the response 200 with a body mapping changed
		// values; we just verify the request shape.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	err := c.UpdateBookmarkLabels(context.Background(), "abc", []string{"new1", "new2"}, []string{"old"})
	if err != nil {
		t.Fatalf("UpdateBookmarkLabels: %v", err)
	}
	_ = log.last(t)
}

func TestUpdateBookmarkLabels_OmittedFieldsAreNotSent(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if _, ok := got["add_labels"]; ok {
			t.Errorf("add_labels should not be sent when empty: %+v", got)
		}
		if _, ok := got["remove_labels"]; ok {
			t.Errorf("remove_labels should not be sent when empty: %+v", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := c.UpdateBookmarkLabels(context.Background(), "abc", nil, nil); err != nil {
		t.Fatalf("UpdateBookmarkLabels: %v", err)
	}
	_ = log.last(t)
}

// --- ListLabels ---------------------------------------------------------

func TestListLabels_ReturnsInventory(t *testing.T) {
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bookmarks/labels" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"name":"tech","count":12,"href":"/api/bookmarks/labels?name=tech"},
			{"name":"news","count":3,"href":"/api/bookmarks/labels?name=news"}
		]`))
	})
	got, err := c.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	if got[0].Name != "tech" || got[0].Count != 12 {
		t.Errorf("got[0]: %+v", got[0])
	}
}

// --- ListCollections ---------------------------------------------------

func TestListCollections_ReturnsAll(t *testing.T) {
	c, _, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bookmarks/collections" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"id":"c1","name":"Tech","labels":"technology,programming","is_pinned":true},
			{"id":"c2","name":"Food","labels":"cooking,recipes","is_pinned":false}
		]`))
	})
	got, err := c.ListCollections(context.Background())
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d", len(got))
	}
	if got[0].Name != "Tech" || got[0].Labels != "technology,programming" {
		t.Errorf("got[0]: %+v", got[0])
	}
	if !got[0].IsPinned {
		t.Errorf("got[0].IsPinned: got false, want true")
	}
}

func TestCreateCollection_SendsNameAndLabels(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/bookmarks/collections" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["name"] != "Tech" {
			t.Errorf("name: got %v, want Tech", got["name"])
		}
		if got["labels"] != "technology,programming" {
			t.Errorf("labels: got %v", got["labels"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new-id","name":"Tech"}`))
	})

	id, err := c.CreateCollection(context.Background(), CollectionCreate{
		Name:   "Tech",
		Labels: "technology,programming",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if id != "new-id" {
		t.Errorf("id: got %q, want %q", id, "new-id")
	}
	_ = log.last(t)
}

func TestUpdateCollection_SendsLabels(t *testing.T) {
	c, _, log := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method: got %q", r.Method)
		}
		if r.URL.Path != "/api/bookmarks/collections/c1" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["labels"] != "tech,ai,llm" {
			t.Errorf("labels: got %v", got["labels"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	err := c.UpdateCollection(context.Background(), "c1", CollectionCreate{
		Name:   "Tech",
		Labels: "tech,ai,llm",
	})
	if err != nil {
		t.Fatalf("UpdateCollection: %v", err)
	}
	_ = log.last(t)
}

// --- Context handling --------------------------------------------------

func TestClient_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client cancels.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "tok", WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.ListBookmarks(ctx, BookmarkListParams{})
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}

// --- helpers -----------------------------------------------------------

func ptrBool(v bool) *bool { return &v }
