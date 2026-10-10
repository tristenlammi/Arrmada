package audioserver

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/listening"
)

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	lib := libraryJSON()
	if strings.Contains(r.URL.Query().Get("include"), "stats") {
		lib["stats"] = s.libraryTotals(r.Context())
	}
	writeJSON(w, http.StatusOK, obj{"libraries": []obj{lib}})
}

// libraryTotals is the library's ?include=stats block. Only files already read count —
// listing libraries mustn't probe the whole library.
func (s *Server) libraryTotals(ctx context.Context) obj {
	items, _ := s.items(ctx)
	var size int64
	var dur float64
	files := 0
	for _, it := range items {
		f, _ := s.probe.files(ctx, it.Path, false)
		size += totalSize(f)
		dur += totalDuration(f)
		files += len(f)
	}
	return obj{"totalSize": size, "totalDuration": dur, "numAudioFiles": files, "totalItems": len(items)}
}

func (s *Server) checkLibrary(w http.ResponseWriter, r *http.Request) bool {
	if !isLibraryID(r.PathValue("lib")) {
		writeError(w, http.StatusNotFound, "Library not found")
		return false
	}
	return true
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	lib := libraryJSON()
	if strings.Contains(r.URL.Query().Get("include"), "stats") {
		lib["stats"] = s.libraryTotals(r.Context())
	}
	out := obj{"library": lib, "issues": 0, "numUserPlaylists": 0}
	if strings.Contains(r.URL.Query().Get("include"), "filterdata") {
		items, err := s.items(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Couldn't read the library")
			return
		}
		out["filterdata"] = filterData(items)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFilterData(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	items, err := s.items(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	writeJSON(w, http.StatusOK, filterData(items))
}

func filterData(items []Item) obj {
	authors := map[string]string{}
	series := map[string]string{}
	genres := map[string]bool{}
	for _, it := range items {
		for _, a := range splitAuthors(it.Book.Author) {
			authors[authorID(a)] = a
		}
		if it.Book.SeriesName != "" {
			series[seriesID(it.Book.SeriesName)] = it.Book.SeriesName
		}
		for _, g := range it.Book.Subjects {
			genres[g] = true
		}
	}
	return obj{
		"authors": namedList(authors), "series": namedList(series), "genres": sortedKeys(genres),
		"tags": []string{}, "narrators": []string{}, "languages": []string{}, "publishers": []string{},
		"publishedDecades": []string{}, "bookCount": len(items), "authorCount": len(authors), "seriesCount": len(series),
		"podcastCount": 0, "numIssues": 0, "loadedAt": 0,
	}
}

func namedList(m map[string]string) []obj {
	out := make([]obj, 0, len(m))
	for id, name := range m {
		out = append(out, obj{"id": id, "name": name})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]["name"].(string)) < strings.ToLower(out[j]["name"].(string))
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Server) progressMap(ctx context.Context, userID int64) map[string]listening.Progress {
	all, _ := s.listen.AllProgress(ctx, userID)
	out := make(map[string]listening.Progress, len(all))
	for _, p := range all {
		out[p.ItemKey] = p
	}
	return out
}

// applyFilter keeps items matching an Audiobookshelf filter ("authors.<base64 id>",
// "series.…", "genres.…", "progress.finished|not-finished|in-progress|not-started").
func applyFilter(items []Item, filter string, prog map[string]listening.Progress) []Item {
	if filter == "" {
		return items
	}
	key, enc, ok := strings.Cut(filter, ".")
	if !ok {
		return items
	}
	// Apps percent-encode the base64 value, and some (ShelfPlayer) encode a value that's
	// already encoded ("…%3D" arrives as "…%253D"), so undo that as Audiobookshelf does; a
	// "+" sent unencoded has become a space by now.
	if u, err := url.PathUnescape(enc); err == nil {
		enc = u
	}
	val := enc
	b64 := strings.ReplaceAll(enc, " ", "+")
	if dec, err := base64.StdEncoding.DecodeString(b64); err == nil {
		val = string(dec)
	} else if dec, err := base64.RawStdEncoding.DecodeString(b64); err == nil {
		val = string(dec)
	}
	out := items[:0:0]
	for _, it := range items {
		keep := true
		switch key {
		case "authors":
			keep = false
			for _, a := range splitAuthors(it.Book.Author) {
				if isAuthorID(val, a) {
					keep = true
				}
			}
		case "series":
			keep = it.Book.SeriesName != "" && isSeriesID(val, it.Book.SeriesName)
		case "narrators":
			keep = false // no narrators are recorded, so none match (rather than all)
		case "genres":
			keep = false
			for _, g := range it.Book.Subjects {
				if g == val {
					keep = true
				}
			}
		case "progress":
			p, has := prog[it.Key]
			switch val {
			case "finished":
				keep = has && p.Finished
			case "not-finished":
				keep = !has || !p.Finished
			case "in-progress":
				keep = has && !p.Finished && p.Position > 0
			case "not-started":
				keep = !has || (p.Position == 0 && !p.Finished)
			}
		}
		if keep {
			out = append(out, it)
		}
	}
	return out
}

func (s *Server) handleLibraryItems(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	prog := s.progressMap(ctx, userOf(r).ID)
	items = applyFilter(items, q.Get("filter"), prog)
	sortKey := q.Get("sort")
	if sortKey == "" {
		sortKey = "media.metadata.title"
	}
	sortItems(items, sortKey, q.Get("desc") == "1" || q.Get("desc") == "true", func(it Item) float64 {
		files, _ := s.probe.files(ctx, it.Path, false)
		return totalDuration(files)
	})
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, _ := strconv.Atoi(q.Get("page"))
	total := len(items)
	pageItems := items
	if limit > 0 {
		start := page * limit
		if start > total {
			start = total
		}
		end := start + limit
		if end > total {
			end = total
		}
		pageItems = items[start:end]
	}
	results := make([]obj, 0, len(pageItems))
	for _, it := range pageItems {
		results = append(results, s.itemMinified(ctx, it, prog))
	}
	writeJSON(w, http.StatusOK, obj{
		"results": results, "total": total, "limit": limit, "page": page, "sortBy": sortKey,
		"sortDesc": q.Get("desc") == "1", "filterBy": q.Get("filter"), "mediaType": "book", "minified": true,
		"collapseseries": false, "include": "", "offset": page * limit,
	})
}

// handlePersonalized builds the home screen: continue listening first.
func (s *Server) handlePersonalized(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	ctx := r.Context()
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	prog := s.progressMap(ctx, userOf(r).ID)
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	shelf := func(id, label, key string, list []Item) obj {
		ents := make([]obj, 0, len(list))
		for _, it := range list {
			ents = append(ents, s.itemMinified(ctx, it, prog))
		}
		return obj{"id": id, "label": label, "labelStringKey": key, "type": "book", "entities": ents, "total": len(ents)}
	}

	// Continue listening: in progress, not finished, not hidden, most recent first.
	var cont, again []Item
	all, _ := s.listen.AllProgress(ctx, userOf(r).ID)
	for _, p := range all {
		it, ok := byKey[p.ItemKey]
		if !ok {
			continue
		}
		if p.Finished {
			again = append(again, it)
		} else if p.Position > 0 && !p.Hidden {
			cont = append(cont, it)
		}
	}
	recent := append([]Item(nil), items...)
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].AddedAt > recent[j].AddedAt })
	if len(recent) > 12 {
		recent = recent[:12]
	}
	var shelves []obj
	if len(cont) > 0 {
		shelves = append(shelves, shelf("continue-listening", "Continue Listening", "LabelContinueListening", limitItems(cont, 20)))
	}
	if next := continueSeries(items, prog); len(next) > 0 {
		shelves = append(shelves, shelf("continue-series", "Continue Series", "LabelContinueSeries", limitItems(next, 12)))
	}
	shelves = append(shelves, shelf("recently-added", "Recently Added", "LabelRecentlyAdded", recent))
	if len(again) > 0 {
		shelves = append(shelves, shelf("listen-again", "Listen Again", "LabelListenAgain", limitItems(again, 12)))
	}
	writeJSON(w, http.StatusOK, shelves)
}

// continueSeries finds, for each series with a finished book, the next unstarted one.
func continueSeries(items []Item, prog map[string]listening.Progress) []Item {
	bySeries := map[string][]Item{}
	for _, it := range items {
		if it.Book.SeriesName != "" && it.Version == nil {
			bySeries[it.Book.SeriesName] = append(bySeries[it.Book.SeriesName], it)
		}
	}
	var out []Item
	for _, list := range bySeries {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Book.SeriesPosition < list[j].Book.SeriesPosition })
		finishedAny := false
		for _, it := range list {
			p, has := prog[it.Key]
			if has && p.Finished {
				finishedAny = true
				continue
			}
			if finishedAny && (!has || p.Position == 0) {
				out = append(out, it)
			}
			break
		}
	}
	return out
}

func limitItems(v []Item, n int) []Item {
	if len(v) > n {
		return v[:n]
	}
	return v
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	ctx := r.Context()
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	type agg struct {
		name  string
		count int
		added int64
	}
	byID := map[string]*agg{}
	for _, it := range items {
		for _, a := range splitAuthors(it.Book.Author) {
			id := authorID(a)
			if byID[id] == nil {
				byID[id] = &agg{name: a}
			}
			byID[id].count++
			if it.AddedAt > byID[id].added {
				byID[id].added = it.AddedAt
			}
		}
	}
	images := s.books.KnownAuthorImages(ctx)
	list := make([]obj, 0, len(byID))
	for id, a := range byID {
		var img any
		if images[a.name] != "" {
			img = "/api/authors/" + id + "/image"
		}
		list = append(list, obj{"id": id, "asin": nil, "name": a.name, "lastFirst": authorNameLF(a.name), "description": nil,
			"imagePath": img, "libraryId": libraryID, "addedAt": a.added, "updatedAt": a.added, "numBooks": a.count})
	}
	q := r.URL.Query()
	desc := q.Get("desc") == "1"
	switch q.Get("sort") {
	case "numBooks":
		sort.SliceStable(list, func(i, j int) bool { return (list[i]["numBooks"].(int) < list[j]["numBooks"].(int)) != desc })
	case "addedAt":
		sort.SliceStable(list, func(i, j int) bool { return (list[i]["addedAt"].(int64) < list[j]["addedAt"].(int64)) != desc })
	default:
		sort.SliceStable(list, func(i, j int) bool {
			return (authorNameLF(list[i]["name"].(string)) < authorNameLF(list[j]["name"].(string))) != desc
		})
	}
	total := len(list)
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, _ := strconv.Atoi(q.Get("page"))
	if limit > 0 {
		start := min(page*limit, total)
		list = list[start:min(start+limit, total)]
	}
	// "results" is the paged shape newer clients (Lissen) read; "authors" the older one.
	writeJSON(w, http.StatusOK, obj{"results": list, "authors": list, "total": total, "limit": limit, "page": page,
		"sortDesc": desc, "minified": false})
}

func (s *Server) handleSeriesList(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	ctx := r.Context()
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	prog := s.progressMap(ctx, userOf(r).ID)
	groups := map[string][]Item{}
	for _, it := range items {
		if it.Book.SeriesName != "" {
			groups[it.Book.SeriesName] = append(groups[it.Book.SeriesName], it)
		}
	}
	list := make([]obj, 0, len(groups))
	for name, g := range groups {
		sortItems(g, "sequence", false, nil)
		books := make([]obj, 0, len(g))
		for _, it := range g {
			books = append(books, s.itemMinified(ctx, it, prog))
		}
		list = append(list, obj{"id": seriesID(name), "name": name, "nameIgnorePrefix": ignorePrefix(name), "description": nil,
			"libraryId": libraryID, "books": books, "addedAt": g[0].AddedAt, "updatedAt": g[0].AddedAt, "totalDuration": 0})
	}
	sort.SliceStable(list, func(i, j int) bool { return sortTitle(list[i]["name"].(string)) < sortTitle(list[j]["name"].(string)) })
	q := r.URL.Query()
	total := len(list)
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, _ := strconv.Atoi(q.Get("page"))
	if limit > 0 {
		start := min(page*limit, total)
		list = list[start:min(start+limit, total)]
	}
	writeJSON(w, http.StatusOK, obj{"results": list, "total": total, "limit": limit, "page": page,
		"sortDesc": q.Get("desc") == "1", "minified": false, "include": ""})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.checkLibrary(w, r) {
		return
	}
	ctx := r.Context()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 12
	}
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	prog := s.progressMap(ctx, userOf(r).ID)
	bookHits := []obj{}
	authors := map[string]string{}
	series := map[string][]Item{}
	for _, it := range items {
		if q == "" {
			break
		}
		title := strings.ToLower(it.Title)
		author := strings.ToLower(it.Book.Author)
		if strings.Contains(title, q) || strings.Contains(author, q) {
			if len(bookHits) < limit {
				match := "title"
				if !strings.Contains(title, q) {
					match = "authors"
				}
				bookHits = append(bookHits, obj{"libraryItem": s.itemMinified(ctx, it, prog), "matchKey": match, "matchText": it.Title})
			}
		}
		for _, a := range splitAuthors(it.Book.Author) {
			if strings.Contains(strings.ToLower(a), q) {
				authors[authorID(a)] = a
			}
		}
		if it.Book.SeriesName != "" && strings.Contains(strings.ToLower(it.Book.SeriesName), q) {
			series[it.Book.SeriesName] = append(series[it.Book.SeriesName], it)
		}
	}
	authorList := namedList(authors)
	for _, a := range authorList {
		a["libraryId"] = libraryID
	}
	seriesList := []obj{}
	for name, g := range series {
		sortItems(g, "sequence", false, nil)
		bks := make([]obj, 0, len(g))
		for _, it := range g {
			bks = append(bks, s.itemMinified(ctx, it, prog))
		}
		seriesList = append(seriesList, obj{"series": obj{"id": seriesID(name), "name": name, "libraryId": libraryID}, "books": bks})
	}
	writeJSON(w, http.StatusOK, obj{"book": bookHits, "authors": limitObjs(authorList, limit), "series": limitObjs(seriesList, limit),
		"narrators": []obj{}, "tags": []obj{}, "genres": []obj{}, "podcast": []obj{}})
}

func limitObjs(v []obj, n int) []obj {
	if len(v) > n {
		return v[:n]
	}
	return v
}

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	it, err := s.item(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	var pp *listening.Progress
	if p, ok, _ := s.listen.Progress(ctx, userOf(r).ID, it.Key); ok {
		pp = &p
	}
	o, _ := s.itemExpanded(ctx, it, pp)
	if pp == nil && strings.Contains(r.URL.Query().Get("include"), "progress") {
		o["userMediaProgress"] = nil // asked for, and there isn't one yet
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleBatchGet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	prog := s.progressMap(ctx, userOf(r).ID)
	out := []obj{}
	// Audiobookshelf answers with full items. At most 100 at a time: a full item reads
	// its files' chapters, which costs more than a list entry.
	for i, id := range body.LibraryItemIDs {
		if i >= 100 {
			break
		}
		if it, err := s.item(ctx, id); err == nil {
			var pp *listening.Progress
			if p, ok := prog[it.Key]; ok {
				pp = &p
			}
			o, _ := s.itemFull(ctx, it, pp, false) // known files only: a batch mustn't wait on ffprobe
			out = append(out, o)
		}
	}
	writeJSON(w, http.StatusOK, obj{"libraryItems": out})
}

func (s *Server) handleAuthor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("aid")
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	prog := s.progressMap(ctx, userOf(r).ID)
	name := ""
	var mine []Item
	for _, it := range items {
		for _, a := range splitAuthors(it.Book.Author) {
			if isAuthorID(id, a) {
				name = a
				mine = append(mine, it)
			}
		}
	}
	if name == "" {
		writeError(w, http.StatusNotFound, "Author not found")
		return
	}
	id = authorID(name) // replies use the classic id, whichever shape was asked for
	var img any
	if s.books.KnownAuthorImages(ctx)[name] != "" {
		img = "/api/authors/" + id + "/image"
	}
	out := obj{"id": id, "asin": nil, "name": name, "description": nil, "imagePath": img, "libraryId": libraryID,
		"addedAt": 0, "updatedAt": 0, "numBooks": len(mine)}
	if strings.Contains(r.URL.Query().Get("include"), "items") {
		sortItems(mine, "media.metadata.publishedYear", false, nil)
		list := make([]obj, 0, len(mine))
		for _, it := range mine {
			list = append(list, s.itemMinified(ctx, it, prog))
		}
		out["libraryItems"] = list
		out["series"] = []obj{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("sid")
	items, err := s.items(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't read the library")
		return
	}
	prog := s.progressMap(ctx, userOf(r).ID)
	var mine []Item
	name := ""
	for _, it := range items {
		if it.Book.SeriesName != "" && isSeriesID(id, it.Book.SeriesName) {
			name = it.Book.SeriesName
			mine = append(mine, it)
		}
	}
	if name == "" {
		writeError(w, http.StatusNotFound, "Series not found")
		return
	}
	id = seriesID(name) // replies use the classic id, whichever shape was asked for
	sortItems(mine, "sequence", false, nil)
	list := make([]obj, 0, len(mine))
	for _, it := range mine {
		list = append(list, s.itemMinified(ctx, it, prog))
	}
	writeJSON(w, http.StatusOK, obj{"id": id, "name": name, "nameIgnorePrefix": ignorePrefix(name), "description": nil,
		"libraryId": libraryID, "addedAt": mine[0].AddedAt, "updatedAt": mine[0].AddedAt, "books": list, "libraryItems": list})
}
