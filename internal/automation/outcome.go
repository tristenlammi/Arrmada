package automation

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/indexer"
)

// SearchOutcome is what one title search found, for the person who pressed Search.
// Before it, Search answered "searching" and the result — a grab, nothing at all, or
// releases for a different film — went only to the log.
type SearchOutcome struct {
	Searched      bool     `json:"searched"`       // an indexer search actually ran
	Returned      int      `json:"returned"`       // releases the indexers returned
	Matching      int      `json:"matching"`       // of those, ones for this title
	Usable        int      `json:"usable"`         // of those, not blocklisted and grabbable
	Grabbed       int      `json:"grabbed"`        // releases sent to the download client
	GrabbedTitles []string `json:"grabbed_titles"` // their names
	Reason        string   `json:"reason"`         // one of the Reason* codes

	// Where the releases went, over the distinct releases seen (searchlog.go): for another
	// title, blocklisted, already downloading, other episodes, turned down by the profile,
	// or fit to take. Reasons counts them by code (Drop* and quality.Reject*); TopReason is
	// the commonest and Example a release it applied to.
	WrongTitle    int               `json:"wrong_title,omitempty"`
	Blocklisted   int               `json:"blocklisted,omitempty"`
	Pending       int               `json:"pending,omitempty"`
	OutOfScope    int               `json:"out_of_scope,omitempty"`
	Rejected      int               `json:"rejected,omitempty"`
	Eligible      int               `json:"eligible,omitempty"`
	Reasons       map[string]int    `json:"reasons,omitempty"`
	TopReason     string            `json:"top_reason,omitempty"`
	Example       string            `json:"example,omitempty"`
	IndexerErrors map[string]string `json:"indexer_errors,omitempty"` // indexer -> redacted error
	AttemptID     int64             `json:"attempt_id,omitempty"`     // its search_attempts row, once stored
}

// Why a search ended the way it did. Other features read these; never rename one.
const (
	ReasonNothingWanted      = "nothing-wanted"                   // nothing monitored is missing; no search ran
	ReasonNoReleases         = "no-releases"                      // the indexers returned nothing
	ReasonNoneForTitle       = "none-for-this-title"              // releases came back, none for this title
	ReasonBlockedOrBelow     = "all-blocklisted-or-below-profile" // matching releases, none grabbable under the profile
	ReasonGrabbed            = "grabbed"
	ReasonAlreadySearching   = "already-searching"   // another search of the same title was running
	ReasonIndexersPaused     = "indexers-paused"     // every indexer is backing off after repeated failures; none was asked
	ReasonIndexersFailed     = "indexers-failed"     // every indexer asked failed
	ReasonNoIndexers         = "no-indexers"         // no enabled indexer serves this kind of search
	ReasonAlreadyDownloading = "already-downloading" // a download for it is in flight (Example names it); nothing was searched
	defaultOutcomeMediaNoun  = "title"
)

// settle fills in Reason from the counts, for a search that ran.
func (o *SearchOutcome) settle() {
	switch {
	case o.Grabbed > 0:
		o.Reason = ReasonGrabbed
	case o.Returned == 0:
		o.Reason = ReasonNoReleases
	case o.Matching == 0:
		o.Reason = ReasonNoneForTitle
	default:
		o.Reason = ReasonBlockedOrBelow
	}
}

// noteSearchErr records why a search that couldn't run ended: when every indexer was
// skipped for backing off, that is the reason — not "no releases" — and nothing was
// actually searched. Every indexer failing, or none serving the search, gets its own
// reason too; other errors leave the outcome alone.
func (o *SearchOutcome) noteSearchErr(err error) {
	switch {
	case indexer.IsPaused(err):
		o.Searched = false
		o.Reason = ReasonIndexersPaused
	case errors.Is(err, indexer.ErrNoIndexers):
		o.Searched = false
		o.Reason = ReasonNoIndexers
	case indexer.IsOutage(err):
		o.Reason = ReasonIndexersFailed
	}
}

// Message is the outcome as one plain sentence for a toast: "Grabbed Dune.Part.Two.2024.
// 2160p.WEB-DL", "No releases found", "12 releases found, none for this movie". noun is
// what was searched ("movie", "show", "book").
func (o SearchOutcome) Message(noun string) string {
	if noun == "" {
		noun = defaultOutcomeMediaNoun
	}
	switch o.Reason {
	case ReasonAlreadySearching:
		return "Already being searched — that search covers it"
	case ReasonNothingWanted:
		return "Nothing to search for — everything monitored is already here"
	case ReasonIndexersPaused:
		return "Not searched — every indexer is paused after repeated failures (see Indexers)"
	case ReasonIndexersFailed:
		return "Every indexer failed (see Indexers)"
	case ReasonNoIndexers:
		return "No indexer is set up for this kind of search"
	case ReasonAlreadyDownloading:
		if o.Example != "" {
			return "Already downloading " + o.Example
		}
		return "Already downloading"
	case ReasonGrabbed:
		if len(o.GrabbedTitles) == 0 {
			return fmt.Sprintf("Grabbed %d %s", o.Grabbed, pluralize(o.Grabbed, "release"))
		}
		msg := "Grabbed " + o.GrabbedTitles[0]
		if n := len(o.GrabbedTitles) - 1; n > 0 {
			msg += fmt.Sprintf(" and %d more", n)
		}
		return msg
	case ReasonNoReleases:
		return "No releases found"
	case ReasonNoneForTitle:
		return fmt.Sprintf("%d %s found, none for this %s", o.Returned, pluralize(o.Returned, "release"), noun)
	case ReasonBlockedOrBelow:
		if o.Usable == 0 && o.Matching > 0 {
			return fmt.Sprintf("%d %s for this %s, all blocklisted or not downloadable", o.Matching, pluralize(o.Matching, "release"), noun)
		}
		n := o.Matching
		if n == 0 {
			n = o.Returned
		}
		return fmt.Sprintf("%d %s found, none met the quality profile", n, pluralize(n, "release"))
	}
	return "Search finished"
}

func pluralize(n int, word string) string {
	if n == 1 || strings.HasSuffix(word, "s") {
		return word
	}
	return word + "s"
}
