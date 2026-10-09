package automation

import (
	"fmt"
	"strings"
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
}

// Why a search ended the way it did.
const (
	ReasonNothingWanted     = "nothing-wanted"                   // nothing monitored is missing; no search ran
	ReasonNoReleases        = "no-releases"                      // the indexers returned nothing
	ReasonNoneForTitle      = "none-for-this-title"              // releases came back, none for this title
	ReasonBlockedOrBelow    = "all-blocklisted-or-below-profile" // matching releases, none grabbable under the profile
	ReasonGrabbed           = "grabbed"
	ReasonAlreadySearching  = "already-searching" // another search of the same title was running
	defaultOutcomeMediaNoun = "title"
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
