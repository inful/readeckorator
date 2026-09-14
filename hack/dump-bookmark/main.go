// SPDX-License-Identifier: GPL-3.0-or-later
//
// dump-bookmark is a one-shot debugging tool. Given a Readeck API
// token (RTOKEN env) and a bookmark ID, it prints the bookmark
// metadata and full article body — the exact payload the
// classifier pipeline sees. Not part of the production binary;
// kept under hack/ for ad-hoc debugging.
//
//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/inful/readeckorator/internal/readeck"
)

func main() {
	token := os.Getenv("RTOKEN")
	if token == "" {
		fmt.Println("usage: RTOKEN=<api-token> go run hack/dump-bookmark/main.go <bookmark-id> [base-url]")
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		fmt.Println("usage: go run hack/dump-bookmark/main.go <bookmark-id>")
		os.Exit(2)
	}
	baseURL := "https://links.luguber.info"
	if len(os.Args) >= 3 {
		baseURL = os.Args[2]
	}
	id := os.Args[1]

	c := readeck.New(baseURL, token)
	ctx := context.Background()

	bm, err := c.GetBookmark(ctx, id)
	if err != nil {
		fmt.Println("GetBookmark:", err)
		os.Exit(1)
	}
	body, err := c.GetArticleMarkdown(ctx, id)
	if err != nil {
		fmt.Println("GetArticleMarkdown:", err)
		os.Exit(1)
	}

	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("Title:       %s\n", bm.Title)
	fmt.Printf("URL:         %s\n", bm.URL)
	fmt.Printf("Site:        %s\n", bm.SiteName)
	fmt.Printf("Lang:        %s\n", bm.Lang)
	fmt.Printf("Type:        %s\n", bm.Type)
	fmt.Printf("Loaded:      %v\n", bm.Loaded)
	fmt.Printf("Labels:      %v\n", bm.Labels)
	fmt.Printf("Description: %s\n", bm.Description)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("Article body (%d bytes):\n", len(body))
	fmt.Println(body)
}
