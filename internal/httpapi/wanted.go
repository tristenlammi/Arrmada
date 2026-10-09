package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/series"
)

// The Wanted view: every monitored title Arrmada is still looking for — movies, series,
// books and albums — with when it last searched, how that went, how many searches in a
// row came up empty, and roughly when the sweep tries next. It answers "why isn't it
// downloading?" on the Downloads page's Searching and Upcoming tabs, and the row and its
// builder are shared by any page that lists wanted titles (the Movies Wanted view reuses
// buildWanted with only movies).

// Wanted row states: what is actually happening to the title, so nothing reads
// "Searching" while it is really waiting on something else.
const (
	wantedSearching = "searching" // the sweep searches it on its ladder
	// wantedUnknown: the download client can't be read, so it may already be downloading,
	// and the sweeps are paused until the client is back. Never "nothing found".
	wantedUnknown = "unknown"
	// wantedWaiting: a download in flight covers everything it is missing (a series whose
	// whole show, or every wanted season, is downloading). Not searched meanwhile.
	wantedWaiting = "waiting_download"
	// wantedHeld: its download finished but is held in Review; it isn't searched again
	// until someone decides.
	wantedHeld = "held_for_review"
	// wantedIndexersFailed: its last search couldn't reach any indexer — not a miss.
	wantedIndexersFailed = "indexers_failed"
	// wantedSlowed: so many empty searches that its sweep now checks it only rarely
	// (books monthly, albums weekly).
	wantedSlowed = "slowed"
	// wantedNotReleased: not out yet, so nothing searches for it (Upcoming rows).
	wantedNotReleased = "not_released"
)

// wantedRow is one title on a Wanted list. It keeps the fields the Downloads page's
// Searching/Upcoming rows always had (movie_id/series_id, title, year, poster_url,
// quality_profile, episode_count, available_at, next_label) and adds the search state.
type wantedRow struct {
	MediaType string `json:"media_type"` // movie | series | book | music
	ID        int64  `json:"id"`         // the movie, series, book or album id
	// The per-kind id the page links by; only the one for MediaType is set.
	MovieID  int64 `json:"movie_id,omitempty"`
	SeriesID int64 `json:"series_id,omitempty"`
	BookID   int64 `json:"book_id,omitempty"`
	AlbumID  int64 `json:"album_id,omitempty"`
	ArtistID int64 `json:"artist_id,omitempty"` // an album's artist, whose page lists it

	Title          string `json:"title"`
	Year           int    `json:"year"`
	PosterURL      string `json:"poster_url,omitempty"`
	QualityProfile string `json:"quality_profile"`
	Byline         string `json:"byline,omitempty"` // a book's author, an album's artist
	// Missing names what a book still lacks: "Ebook", "Audiobook", "Audio version".
	Missing      []string `json:"missing,omitempty"`
	EpisodeCount int      `json:"episode_count,omitempty"` // series: aired, monitored, missing episodes

	State string `json:"state"`
	// WaitingOn is the release a waiting row is waiting for; Stalled when the client says
	// nobody is sending it data. WaitingNote names seasons downloading while the rest of
	// the show is still searched ("S03 downloading").
	WaitingOn   string `json:"waiting_on,omitempty"`
	Stalled     bool   `json:"stalled,omitempty"`
	WaitingNote string `json:"waiting_note,omitempty"`
	ReviewID    int64  `json:"review_id,omitempty"` // a pending review for the title

	// The sweep's own backoff state. LastSearchAt is RFC 3339 ("" = never searched).
	// NextSearchAt (RFC 3339) is when the next automatic search is due — now or earlier
	// means on the sweep's next run (Due). Absent when no automatic search is coming:
	// waiting on a download, held, not released, or the client is down.
	LastSearchAt string `json:"last_search_at,omitempty"`
	SearchMisses int    `json:"search_misses"`
	NextSearchAt string `json:"next_search_at,omitempty"`
	Due          bool   `json:"due,omitempty"`
	// LastSearch is the stored attempts in brief: the latest, the empty run, the main
	// reason (automation.LatestAttempts). Absent until the title has been searched.
	LastSearch *automation.AttemptSummary `json:"last_search,omitempty"`

	AvailableAt string `json:"available_at,omitempty"` // Upcoming: release or air date
	NextLabel   string `json:"next_label,omitempty"`   // Upcoming series: "S02E13"
}

// wantedLists is the Wanted view: titles the sweeps search for, and titles not out yet.
type wantedLists struct {
	Searching []wantedRow `json:"searching"`
	Upcoming  []wantedRow `json:"upcoming"`
	// QueueKnown is whether the download client could be read just now. When it couldn't,
	// rows read "unknown" and the sweeps are paused (ACQ-22/23).
	QueueKnown bool `json:"queue_known"`
}

// wantedKinds picks which media types buildWanted lists; nil lists all of them.
type wantedKinds map[string]bool

func (k wantedKinds) has(kind string) bool { return k == nil || k[kind] }

// wantedBuild is what every row of one build shares: one queue read, one reviews read,
// one profile lookup per profile.
type wantedBuild struct {
	a          *api
	ctx        context.Context
	now        time.Time
	queue      []download.Item
	queueKnown bool
	// untracked is the unfinished torrents no grab knows by hash (added by hand or by
	// another tool): the sweeps still hold a title back for one named like it.
	untracked []download.Item
	reviews    map[string]int64 // "<kind>:<id>" → a pending review's id
	profiles   map[string]string
}

func (b *wantedBuild) profile(ref string) string {
	if v, ok := b.profiles[ref]; ok {
		return v
	}
	v := b.a.profileName(b.ctx, ref)
	b.profiles[ref] = v
	return v
}

// schedule fills the row's sweep state from its stored last search and misses.
func (b *wantedBuild) schedule(row *wantedRow, lastAt string, misses int) {
	slot := automation.SweepSchedule(row.MediaType, lastAt, misses)
	row.SearchMisses = misses
	if !slot.Last.IsZero() {
		row.LastSearchAt = slot.Last.UTC().Format(time.RFC3339)
	}
	next := slot.Next
	if !next.After(b.now) {
		next, row.Due = b.now, true
	}
	row.NextSearchAt = next.UTC().Format(time.RFC3339)
	if slot.Slowed && row.State == wantedSearching {
		row.State = wantedSlowed
	}
}

// settle applies what overrides "searching", once the row's last search is known: a
// client that can't be read, or indexers that all failed last time. A row that isn't
// going to be searched automatically loses its next-search time.
func (b *wantedBuild) settle(row *wantedRow) {
	switch {
	case row.State == wantedHeld || row.State == wantedWaiting:
	case !b.queueKnown:
		row.State = wantedUnknown
	case row.LastSearch != nil && row.LastSearch.Latest.Outcome == automation.OutcomeIndexersFailed:
		row.State = wantedIndexersFailed
	}
	if row.State == wantedHeld || row.State == wantedWaiting || row.State == wantedUnknown {
		row.NextSearchAt, row.Due = "", false
	}
}

// buildWanted builds the Wanted view for the given kinds (nil: all four). Books are left
// out while the Books module is off, and albums unless Music is on. Database reads and
// one shared queue read; no indexer is asked anything.
func (a *api) buildWanted(ctx context.Context, kinds wantedKinds) wantedLists {
	snap, known, _ := a.queueSnapshot(ctx)
	b := &wantedBuild{a: a, ctx: ctx, now: time.Now().UTC(), queue: snap.Items, queueKnown: known,
		reviews: map[string]int64{}, profiles: map[string]string{}}
	if a.deps.Automation != nil {
		if list, err := a.deps.Automation.ListReviews(ctx); err == nil {
			for _, rv := range list { // newest first: the first one per title wins
				key := fmt.Sprintf("%s:%d", rv.MediaType, rv.ExpectedID)
				if _, ok := b.reviews[key]; !ok && rv.ExpectedID > 0 {
					b.reviews[key] = rv.ID
				}
			}
		}
	}
	if a.deps.Automation != nil && known {
		b.untracked, _ = a.deps.Automation.UntrackedQueue(ctx, b.queue)
	}
	out := wantedLists{Searching: []wantedRow{}, Upcoming: []wantedRow{}, QueueKnown: known}
	add := func(searching, upcoming []wantedRow) {
		out.Searching = append(out.Searching, searching...)
		out.Upcoming = append(out.Upcoming, upcoming...)
	}
	if kinds.has(automation.AttemptMovie) && a.deps.Movies != nil {
		add(b.movies())
	}
	if kinds.has(automation.AttemptSeries) && a.deps.Series != nil {
		add(b.series())
	}
	if kinds.has(automation.AttemptBook) && a.deps.Books != nil && a.booksEnabled(ctx) {
		add(b.books(), nil)
	}
	if kinds.has(automation.AttemptMusic) && a.deps.Music != nil && a.musicEnabled(ctx) {
		add(b.albums())
	}
	return out
}

// active is the in-flight acquisitions of one media type, keyed by item; nil without a
// coordinator or when the record can't be read (then nothing is known to be in flight,
// and the rows say what the sweep's state says).
func (b *wantedBuild) active(kind string) map[int64][]automation.Acquisition {
	if b.a.deps.Automation == nil {
		return nil
	}
	m, err := b.a.deps.Automation.ActiveByItem(b.ctx, kind)
	if err != nil {
		b.a.deps.Log.Warn("wanted: couldn't read what's downloading", "kind", kind, "err", err)
		return nil
	}
	return m
}

// heldOnly sorts a title's in-flight acquisitions: downloading is true when any is still
// on its way (the title is on the Downloads tab, not Wanted); held when every one is
// finished and waiting in Review.
func heldOnly(acqs []automation.Acquisition) (downloading, held bool) {
	for _, acq := range acqs {
		if automation.AcqHeld(acq) {
			held = true
			continue
		}
		return true, false
	}
	return false, held
}

// attach adds each row's stored-attempt summary, one query for the kind, then settles
// its state.
func (b *wantedBuild) attach(kind string, rows []wantedRow) {
	if len(rows) == 0 {
		return
	}
	if b.a.deps.Automation != nil {
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if sums, err := b.a.deps.Automation.LatestAttempts(b.ctx, kind, ids); err == nil {
			for i := range rows {
				if s, ok := sums[rows[i].ID]; ok {
					rows[i].LastSearch = &s
				}
			}
		}
	}
	for i := range rows {
		b.settle(&rows[i])
	}
}

// movies: monitored films with no file. Past their minimum availability they are searched;
// before it they are Upcoming. A film with a download in flight is on the Downloads tab
// instead; one whose download is held in Review says so.
func (b *wantedBuild) movies() (searching, upcoming []wantedRow) {
	list, err := b.a.deps.Movies.List(b.ctx)
	if err != nil {
		return nil, nil
	}
	active := b.active(automation.AttemptMovie)
	states, _ := b.a.deps.Movies.SearchStates(b.ctx)
	for _, m := range list {
		if !m.Monitored || m.HasFile {
			continue
		}
		downloading, held := heldOnly(active[m.ID])
		if downloading {
			continue
		}
		row := wantedRow{MediaType: automation.AttemptMovie, ID: m.ID, MovieID: m.ID, Title: m.Title, Year: m.Year,
			PosterURL: m.PosterURL, QualityProfile: b.profile(m.QualityProfile), State: wantedSearching}
		if !b.a.deps.Movies.IsAvailable(m) {
			row.State = wantedNotReleased
			if m.Extra != nil && m.Extra.ReleaseDate != "" {
				row.AvailableAt = m.Extra.ReleaseDate
			}
			upcoming = append(upcoming, row)
			continue
		}
		st := states[m.ID]
		b.schedule(&row, st.LastAt, st.Misses)
		if held {
			row.State, row.ReviewID = wantedHeld, b.reviews[fmt.Sprintf("movie:%d", m.ID)]
		} else if name := automation.UntrackedMovieItem(b.untracked, m); name != "" {
			// The sweep leaves it alone while a torrent named for it is in the client.
			row.State, row.WaitingOn = wantedWaiting, name
		}
		searching = append(searching, row)
	}
	b.attach(automation.AttemptMovie, searching)
	return searching, upcoming
}

// series: monitored shows with aired, monitored, missing episodes, each one row with the
// count; the soonest unaired episode is its Upcoming row. A show whose every wanted season
// (or the whole show) is covered by a download still in flight is waiting on it, by the
// same rule the sweep holds seasons back by; a partial cover is noted on the row.
func (b *wantedBuild) series() (searching, upcoming []wantedRow) {
	active := b.active(automation.AttemptSeries)
	untrackedTV := false
	for _, it := range b.untracked {
		if it.Category == download.CategoryTV {
			untrackedTV = true
			break
		}
	}
	for _, sa := range b.a.deps.Series.AcquisitionSummary(b.ctx) {
		if sa.NextAir != "" {
			upcoming = append(upcoming, wantedRow{MediaType: automation.AttemptSeries, ID: sa.ID, SeriesID: sa.ID,
				Title: sa.Title, Year: sa.Year, PosterURL: sa.PosterURL, QualityProfile: b.profile(sa.QualityProfile),
				State: wantedNotReleased, AvailableAt: sa.NextAir, NextLabel: sa.NextLabel})
		}
		if sa.SearchingCount == 0 {
			continue
		}
		row := wantedRow{MediaType: automation.AttemptSeries, ID: sa.ID, SeriesID: sa.ID, Title: sa.Title, Year: sa.Year,
			PosterURL: sa.PosterURL, QualityProfile: b.profile(sa.QualityProfile), EpisodeCount: sa.SearchingCount,
			State: wantedSearching}
		b.schedule(&row, sa.LastSearchAt, sa.SearchMisses)
		acqs := active[sa.ID]
		for _, acq := range acqs {
			if automation.AcqHeld(acq) {
				// A held pack doesn't stop the sweep (it holds no season), so the row is
				// still searching; the review is linked for whoever wants to settle it.
				row.ReviewID = b.reviews[fmt.Sprintf("series:%d", sa.ID)]
			}
		}
		if len(acqs) > 0 || untrackedTV {
			b.seriesInFlight(&row, sa, acqs, b.untracked)
		}
		searching = append(searching, row)
	}
	b.attach(automation.AttemptSeries, searching)
	return searching, upcoming
}

// seriesInFlight marks a show waiting on its downloads when they cover every season it's
// missing, and notes the seasons downloading when they cover only some.
func (b *wantedBuild) seriesInFlight(row *wantedRow, sa series.SeriesAcquisition, acqs []automation.Acquisition, untracked []download.Item) {
	s, err := b.a.deps.Series.Get(b.ctx, sa.ID)
	if err != nil {
		return
	}
	seasons, whole, names := automation.SeriesInFlightScope(acqs, untracked, s)
	if len(names) == 0 {
		return
	}
	var held []string
	all := true
	for _, sn := range sa.WantedSeasons {
		if seasons[sn] {
			held = append(held, fmt.Sprintf("S%02d", sn))
		} else {
			all = false
		}
	}
	if whole || (all && len(held) > 0) {
		row.State, row.WaitingOn = wantedWaiting, names[0]
		for _, acq := range acqs {
			if !automation.AcqHeld(acq) && automation.AcqStalled(acq) {
				row.Stalled = true
			}
		}
		return
	}
	if len(held) > 0 {
		row.WaitingNote = strings.Join(held, ", ") + " downloading"
	}
}

// books: monitored books missing an edition (or monitored audio version) their profile
// wants. Books have no release date to wait on, so there is no Upcoming half.
func (b *wantedBuild) books() []wantedRow {
	list, err := b.a.deps.Books.List(b.ctx)
	if err != nil {
		return nil
	}
	active := b.active(automation.AttemptBook)
	wants := map[string][2]bool{}
	var rows []wantedRow
	for _, bk := range list {
		if !bk.Monitored {
			continue
		}
		w, ok := wants[bk.QualityProfile]
		if !ok {
			w = b.a.bookWants(b.ctx, bk.QualityProfile)
			wants[bk.QualityProfile] = w
		}
		bk.WantEbook, bk.WantAudiobook = w[0], w[1]
		if !bk.LacksWanted() {
			continue
		}
		downloading, held := heldOnly(active[bk.ID])
		if downloading {
			continue
		}
		row := wantedRow{MediaType: automation.AttemptBook, ID: bk.ID, BookID: bk.ID, Title: bk.Title, Year: bk.Year,
			PosterURL: bk.CoverURL, QualityProfile: b.profile(bk.QualityProfile), Byline: bk.Author,
			Missing: bookMissing(bk), State: wantedSearching}
		b.schedule(&row, bk.LastSearchAt, bk.SearchMisses)
		if held {
			row.State, row.ReviewID = wantedHeld, b.reviews[fmt.Sprintf("book:%d", bk.ID)]
		}
		rows = append(rows, row)
	}
	b.attach(automation.AttemptBook, rows)
	return rows
}

// bookMissing names what a book still lacks, in the words its page uses.
func bookMissing(bk books.Book) []string {
	var out []string
	if bk.WantEbook && bk.Ebook == nil {
		out = append(out, "Ebook")
	}
	if bk.WantAudiobook && bk.Audiobook == nil {
		out = append(out, "Audiobook")
	}
	for _, v := range bk.AudioVersions {
		if v.Monitored && v.File == nil && len(v.Terms) > 0 {
			out = append(out, "Audio version")
			break
		}
	}
	return out
}

// bookWants is which editions a profile wants, as the book pages read it (an unreadable
// profile wants the ebook).
func (a *api) bookWants(ctx context.Context, ref string) [2]bool {
	if sp, err := a.deps.Quality.GetStored(ctx, ref); err == nil {
		e, au := books.WantedEditions(sp.FormatScores)
		return [2]bool{e, au}
	}
	return [2]bool{true, false}
}

// albums: monitored, incomplete albums of monitored artists. One not released yet is
// Upcoming, by the music sweep's own date rule.
func (b *wantedBuild) albums() (searching, upcoming []wantedRow) {
	albums, err := b.a.deps.Music.WantedAlbums(b.ctx)
	if err != nil {
		return nil, nil
	}
	artists, err := b.a.deps.Music.ListArtists(b.ctx)
	if err != nil {
		return nil, nil
	}
	byID := make(map[int64]music.Artist, len(artists))
	for _, ar := range artists {
		byID[ar.ID] = ar
	}
	active := b.active(automation.AttemptMusic)
	today := b.now.Format("2006-01-02")
	for _, al := range albums {
		ar, ok := byID[al.ArtistID]
		if !ok || al.Complete() {
			continue
		}
		downloading, held := heldOnly(active[al.ID])
		if downloading {
			continue
		}
		row := wantedRow{MediaType: automation.AttemptMusic, ID: al.ID, AlbumID: al.ID, ArtistID: ar.ID, Title: al.Title,
			Year: al.Year, PosterURL: al.CoverURL, QualityProfile: b.profile(ar.QualityProfile), Byline: ar.Name,
			State: wantedSearching}
		if !automation.AlbumReleased(al, today) {
			row.State, row.AvailableAt = wantedNotReleased, al.ReleaseDate
			if row.AvailableAt == "" && al.Year > 0 {
				row.AvailableAt = fmt.Sprintf("%04d", al.Year)
			}
			upcoming = append(upcoming, row)
			continue
		}
		b.schedule(&row, al.LastSearchAt, al.SearchMisses)
		if held {
			row.State, row.ReviewID = wantedHeld, b.reviews[fmt.Sprintf("music:%d", al.ID)]
		}
		searching = append(searching, row)
	}
	b.attach(automation.AttemptMusic, searching)
	return searching, upcoming
}

// handleWanted is GET /api/v1/wanted: the Wanted view for the Downloads page's Searching
// and Upcoming tabs. ?kind=movie (or series, book, music; repeatable) narrows it.
func (a *api) handleWanted(w http.ResponseWriter, r *http.Request) {
	var kinds wantedKinds
	if ks := r.URL.Query()["kind"]; len(ks) > 0 {
		kinds = wantedKinds{}
		for _, k := range ks {
			if !automation.ValidAttemptKind(k) {
				a.writeError(w, http.StatusBadRequest, "kind must be movie, series, book or music")
				return
			}
			kinds[k] = true
		}
	}
	a.writeJSON(w, http.StatusOK, a.buildWanted(r.Context(), kinds))
}

// errWantedGone is a Search now for a title that isn't in the library (or whose module
// is off).
var errWantedGone = errors.New("not in the library")

// wantedSearchJob is the search a Wanted row's Search now starts, after checking the
// title exists: the same job its own page's Search button runs (so a click on either
// while the other runs gets that job back), and the function that resets its sweep
// backoff.
func (a *api) wantedSearchJob(ctx context.Context, kind string, id int64) (jobs.Spec, func(), error) {
	switch kind {
	case automation.AttemptMovie:
		if _, err := a.deps.Movies.Get(ctx, id); err != nil {
			return jobs.Spec{}, nil, notFoundOr(err, movies.ErrNotFound)
		}
		return a.movieManualSearchJob(id), func() { a.deps.Movies.ResetSearchMisses(ctx, id) }, nil
	case automation.AttemptSeries:
		if a.deps.Series == nil {
			return jobs.Spec{}, nil, errWantedGone
		}
		if _, err := a.deps.Series.Get(ctx, id); err != nil {
			return jobs.Spec{}, nil, notFoundOr(err, series.ErrNotFound)
		}
		return a.seriesManualSearchJob(id), func() { a.deps.Series.ResetSearchMisses(ctx, id) }, nil
	case automation.AttemptBook:
		if a.deps.Books == nil || !a.booksEnabled(ctx) {
			return jobs.Spec{}, nil, errWantedGone
		}
		if _, err := a.deps.Books.Get(ctx, id); err != nil {
			return jobs.Spec{}, nil, notFoundOr(err, books.ErrNotFound)
		}
		return a.bookSearchJob(id), func() { a.deps.Books.ResetSearchMisses(ctx, id) }, nil
	case automation.AttemptMusic:
		if a.deps.Music == nil || !a.musicEnabled(ctx) {
			return jobs.Spec{}, nil, errWantedGone
		}
		if _, err := a.deps.Music.AlbumRecord(ctx, id); err != nil {
			return jobs.Spec{}, nil, notFoundOr(err, music.ErrNotFound)
		}
		return a.albumSearchJob(id), func() {
			if err := a.deps.Music.ResetSearchMisses(ctx, id); err != nil {
				a.deps.Log.Warn("wanted: couldn't reset an album's search backoff", "album", id, "err", err)
			}
		}, nil
	}
	return jobs.Spec{}, nil, errBadKind
}

var errBadKind = errors.New("kind must be movie, series, book or music")

// notFoundOr reads a getter's error: the package's not-found error is errWantedGone,
// anything else is passed on.
func notFoundOr(err, notFound error) error {
	if errors.Is(err, notFound) {
		return errWantedGone
	}
	return err
}

// handleWantedSearch is POST /api/v1/wanted/{kind}/{id}/search: Search now from a Wanted
// row. It clears the title's sweep backoff, so the sweep goes back to looking at it
// often, and runs its search now as a job (202 with job_id; the page follows it with
// useJob and the stored attempt). started_at_ms finds this search's attempt later.
func (a *api) handleWantedSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if a.deps.Automation == nil {
		a.writeError(w, http.StatusServiceUnavailable, "searching isn't available")
		return
	}
	spec, reset, err := a.wantedSearchJob(r.Context(), r.PathValue("kind"), id)
	switch {
	case errors.Is(err, errBadKind):
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, errWantedGone):
		a.writeError(w, http.StatusNotFound, "that title isn't in your library")
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not start that search")
		return
	}
	started := time.Now().UnixMilli()
	reset()
	jobID, existing, ok := a.submitOr503(w, r, spec)
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching", "started_at_ms": started})
}
