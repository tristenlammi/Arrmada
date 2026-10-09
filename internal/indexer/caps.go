package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Caps is what a Torznab/Newznab indexer says it supports (its t=caps answer): which
// kinds of search it takes and with which parameters, its categories (subcategories
// included) and its result limits.
type Caps struct {
	Search        SearchMode `json:"search"`
	TV            SearchMode `json:"tv"`
	Movie         SearchMode `json:"movie"`
	Music         SearchMode `json:"music"`
	Audio         SearchMode `json:"audio"`
	Book          SearchMode `json:"book"`
	Categories    []int      `json:"categories,omitempty"`
	LimitsMax     int        `json:"limits_max,omitempty"`
	LimitsDefault int        `json:"limits_default,omitempty"`
}

// SearchMode is one kind of search an indexer takes and the parameters it understands.
type SearchMode struct {
	Available bool     `json:"available"`
	Params    []string `json:"params,omitempty"`
}

// CapsTester is a searcher whose Test also reads what the indexer supports.
type CapsTester interface {
	TestCaps(ctx context.Context, idx Indexer) (Caps, error)
}

// TorznabError is an indexer's own <error code="…" description="…"/> answer — a wrong
// API key, a disabled indexer, a hit limit. Its text is the indexer's description, which
// says what's wrong far better than the XML parse error it used to become.
type TorznabError struct {
	Code        int
	Description string
}

func (e *TorznabError) Error() string {
	if e.Description != "" {
		return e.Description
	}
	return fmt.Sprintf("indexer error %d", e.Code)
}

// errWebPage is an answer that isn't a Torznab API at all — most often Prowlarr's own web
// page, which its root URL serves with HTTP 200.
var errWebPage = errors.New("This is a web page, not a Torznab API; the URL should end in /api (for Prowlarr: http://host:9696/<id>/api)")

// xmlRoot reads the first element of an answer: its lower-case name and attributes. HTML
// is read leniently so a web page is recognised as one rather than failing to parse.
func xmlRoot(data []byte) (string, []xml.Attr, *xml.Decoder, bool) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	for {
		tok, err := d.Token()
		if err != nil {
			return "", nil, nil, false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return strings.ToLower(se.Name.Local), se.Attr, d, true
		}
	}
}

// attr reads an attribute by name, ignoring case: Jackett and Prowlarr builds differ.
func attr(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value
		}
	}
	return ""
}

// torznabErrorFrom builds the error an <error> root carries.
func torznabErrorFrom(attrs []xml.Attr) *TorznabError {
	return &TorznabError{Code: atoi(attr(attrs, "code")), Description: strings.TrimSpace(attr(attrs, "description"))}
}

// parseCaps reads a t=caps answer. A root <error> is the indexer's TorznabError; HTML or
// anything else that isn't <caps> is errWebPage.
func parseCaps(data []byte) (Caps, error) {
	root, attrs, d, ok := xmlRoot(data)
	switch {
	case !ok:
		return Caps{}, errWebPage
	case root == "error":
		return Caps{}, torznabErrorFrom(attrs)
	case root != "caps":
		return Caps{}, errWebPage
	}

	var c Caps
	seen := map[int]bool{}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Caps{}, fmt.Errorf("couldn't read the indexer's capabilities: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch name := strings.ToLower(se.Name.Local); name {
		case "limits":
			c.LimitsMax, c.LimitsDefault = atoi(attr(se.Attr, "max")), atoi(attr(se.Attr, "default"))
		case "search", "tv-search", "movie-search", "music-search", "audio-search", "book-search":
			m := SearchMode{Available: strings.EqualFold(strings.TrimSpace(attr(se.Attr, "available")), "yes")}
			for _, p := range strings.Split(attr(se.Attr, "supportedParams"), ",") {
				if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
					m.Params = append(m.Params, p)
				}
			}
			switch name {
			case "search":
				c.Search = m
			case "tv-search":
				c.TV = m
			case "movie-search":
				c.Movie = m
			case "music-search":
				c.Music = m
			case "audio-search":
				c.Audio = m
			case "book-search":
				c.Book = m
			}
		case "category", "subcat":
			if id, err := strconv.Atoi(strings.TrimSpace(attr(se.Attr, "id"))); err == nil && id > 0 && !seen[id] {
				seen[id] = true
				c.Categories = append(c.Categories, id)
			}
		}
	}
	sort.Ints(c.Categories)
	return c, nil
}

// Summary is the one line the Indexers page shows about what an indexer supports:
// "Movies (imdbid, tmdbid) · TV (tvdbid, season, ep) · 23 categories". The plain q
// parameter every mode takes isn't listed.
func (c Caps) Summary() string {
	var parts []string
	for _, m := range []struct {
		label string
		mode  SearchMode
	}{{"Movies", c.Movie}, {"TV", c.TV}, {"Music", c.Music}, {"Audio", c.Audio}, {"Books", c.Book}} {
		if !m.mode.Available {
			continue
		}
		var params []string
		for _, p := range m.mode.Params {
			if p != "q" {
				params = append(params, p)
			}
		}
		if len(params) > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", m.label, strings.Join(params, ", ")))
		} else {
			parts = append(parts, m.label)
		}
	}
	if n := len(c.Categories); n > 0 {
		label := "categories"
		if n == 1 {
			label = "category"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, label))
	}
	return strings.Join(parts, " · ")
}

// CapsSummary is Caps.Summary for what was last stored; "" when nothing has been read.
func (i Indexer) CapsSummary() string {
	if i.CapsJSON == "" {
		return ""
	}
	var c Caps
	if json.Unmarshal([]byte(i.CapsJSON), &c) != nil {
		return ""
	}
	return c.Summary()
}
