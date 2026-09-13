// SPDX-License-Identifier: GPL-3.0-or-later

package readeck

import (
	"net/url"
	"strconv"
)

// Bookmark is the JSON shape returned by GET /api/bookmarks and
// GET /api/bookmarks/{id}. Readeck returns many more fields than
// we declare here — we only model the ones this application
// actually uses. Unknown fields are silently ignored on decode.
type Bookmark struct {
	ID         string   `json:"id"`
	Href       string   `json:"href,omitempty"`
	Created    string   `json:"created,omitempty"`
	Updated    string   `json:"updated,omitempty"`
	State      int      `json:"state,omitempty"`
	Loaded     bool     `json:"loaded"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	SiteName   string   `json:"site_name,omitempty"`
	Site       string   `json:"site,omitempty"`
	Published  string   `json:"published,omitempty"`
	Authors    []string `json:"authors,omitempty"`
	Lang       string   `json:"lang,omitempty"`
	Type       string   `json:"type,omitempty"`        // article | photo | video
	HasArticle bool     `json:"has_article,omitempty"`
	IsMarked   bool     `json:"is_marked,omitempty"`
	IsArchived bool     `json:"is_archived,omitempty"`
	Labels     []string `json:"labels"`
	WordCount  int      `json:"word_count,omitempty"`
}

// BookmarkListParams captures every filter the application exposes
// to the LLM/classifier pipeline. Zero-valued fields are omitted
// from the request so the server applies its own defaults.
type BookmarkListParams struct {
	// Limit is the page size (Readeck uses "limit" not "page_size").
	Limit int `json:"limit,omitempty"`
	// Page is the 1-based page index. The server's pagination is
	// also discoverable via Link headers.
	Page int `json:"page,omitempty"`
	// Sort is one of "created", "-created", "updated", "-updated".
	Sort string `json:"sort,omitempty"`
	// Search is a full-text query string.
	Search string `json:"search,omitempty"`
	// HasLabels filters by whether the bookmark has any labels
	// (true), no labels (false), or any value (nil → omitted).
	HasLabels *bool `json:"has_labels,omitempty"`
	// IsLoaded filters by extraction state.
	IsLoaded *bool `json:"is_loaded,omitempty"`
	// IsArchived filters by archive state.
	IsArchived *bool `json:"is_archived,omitempty"`
	// Labels is a comma-separated label filter (server-side).
	Labels string `json:"labels,omitempty"`
}

// queryValues renders the params as url.Values, omitting zero/nil
// entries.
func (p BookmarkListParams) queryValues() url.Values {
	v := url.Values{}
	if p.Limit != 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Page != 0 {
		v.Set("page", strconv.Itoa(p.Page))
	}
	if p.Sort != "" {
		v.Set("sort", p.Sort)
	}
	if p.Search != "" {
		v.Set("search", p.Search)
	}
	if p.HasLabels != nil {
		v.Set("has_labels", strconv.FormatBool(*p.HasLabels))
	}
	if p.IsLoaded != nil {
		v.Set("is_loaded", strconv.FormatBool(*p.IsLoaded))
	}
	if p.IsArchived != nil {
		v.Set("is_archived", strconv.FormatBool(*p.IsArchived))
	}
	if p.Labels != "" {
		v.Set("labels", p.Labels)
	}
	return v
}

// Label is one entry in the Readeck label inventory.
type Label struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Href  string `json:"href,omitempty"`
}

// Collection describes one saved-search collection in Readeck.
type Collection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPinned  bool   `json:"is_pinned"`
	IsDeleted bool   `json:"is_deleted,omitempty"`
	Search    string `json:"search,omitempty"`
	Title     string `json:"title,omitempty"`
	Author    string `json:"author,omitempty"`
	Site      string `json:"site,omitempty"`
	// Labels is the raw string the Readeck UI uses in the labels
	// filter (e.g. "technology,programming"). Empty when no labels
	// are filtered.
	Labels     string   `json:"labels,omitempty"`
	Type       []string `json:"type,omitempty"`
	ReadStatus []string `json:"read_status,omitempty"`
	IsMarked   *bool    `json:"is_marked,omitempty"`
	IsArchived *bool    `json:"is_archived,omitempty"`
	RangeStart string   `json:"range_start,omitempty"`
	RangeEnd   string   `json:"range_end,omitempty"`
}

// CollectionCreate is the payload for POST /api/bookmarks/collections
// and PATCH /api/bookmarks/collections/{id}. Only the fields this
// app sets are populated; the rest stay zero.
type CollectionCreate struct {
	Name   string `json:"name"`
	Labels string `json:"labels,omitempty"`
}
