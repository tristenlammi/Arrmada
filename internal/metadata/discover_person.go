package metadata

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// A person's page on Discover: who they are and what they've made. Person credits are
// where adult titles most often leak (an actor's early work, a performer's catalogue), so
// every credit goes through toItem like every other card, and a person TMDB flags adult
// is not found at all.

// DiscoverPeople is the optional capability behind person pages.
type DiscoverPeople interface {
	Person(ctx context.Context, id int) (*Person, error)
}

// Person is one person's page.
type Person struct {
	ID                 int            `json:"id"`
	Name               string         `json:"name"`
	Biography          string         `json:"biography,omitempty"`
	ProfileURL         string         `json:"profile_url,omitempty"`
	Birthday           string         `json:"birthday,omitempty"`
	PlaceOfBirth       string         `json:"place_of_birth,omitempty"`
	KnownForDepartment string         `json:"known_for_department,omitempty"`
	Credits            []DiscoverItem `json:"credits"`
	// Adult is TMDB's flag; such a person is ErrNotFound, never shown. Serialised only so
	// the disk cache keeps it.
	Adult bool `json:"adult,omitempty"`
}

const (
	personTTL        = 24 * time.Hour
	personMinVotes   = 10  // a low floor: a real but small film is still their work
	personMaxCredits = 200 // a prolific character actor's whole list is plenty
)

// personCrewJobs are the crew credits a filmography lists: what they directed or created.
var personCrewJobs = map[string]bool{"Director": true, "Creator": true}

// Person fetches a person with their combined movie and TV credits. Kept for a day (stale
// served while it refreshes). An adult person is ErrNotFound.
func (t *TMDB) Person(ctx context.Context, id int) (*Person, error) {
	if id <= 0 {
		return nil, ErrNotFound
	}
	p, err := swr(ctx, t.disk, "tmdb:person:v1:"+strconv.Itoa(id), personTTL, func(ctx context.Context) (*Person, error) {
		q := url.Values{}
		q.Set("append_to_response", "combined_credits,external_ids")
		body, err := t.get(ctx, "/person/"+strconv.Itoa(id), q)
		if err != nil {
			return nil, err
		}
		return parsePerson(body, t.genreNames(ctx))
	})
	if err != nil {
		return nil, err
	}
	if p == nil || p.Adult {
		return nil, ErrNotFound
	}
	return p, nil
}

// parsePerson maps TMDB's /person answer: credits (cast, plus directing and creating)
// through toItem with a low vote floor, one card per title, most popular first.
func parsePerson(body []byte, genres map[int]string) (*Person, error) {
	var raw struct {
		ID                 int    `json:"id"`
		Name               string `json:"name"`
		Biography          string `json:"biography"`
		ProfilePath        string `json:"profile_path"`
		Birthday           string `json:"birthday"`
		PlaceOfBirth       string `json:"place_of_birth"`
		KnownForDepartment string `json:"known_for_department"`
		Adult              bool   `json:"adult"`
		CombinedCredits    struct {
			Cast []tmdbDiscoverItem `json:"cast"`
			Crew []struct {
				tmdbDiscoverItem
				Job string `json:"job"`
			} `json:"crew"`
		} `json:"combined_credits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	p := &Person{
		ID: raw.ID, Name: raw.Name, Biography: raw.Biography, Birthday: raw.Birthday, PlaceOfBirth: raw.PlaceOfBirth,
		KnownForDepartment: raw.KnownForDepartment, Adult: raw.Adult, Credits: []DiscoverItem{},
	}
	if raw.ProfilePath != "" {
		p.ProfileURL = tmdbProfileBase + raw.ProfilePath
	}
	if raw.Adult {
		return p, nil // nothing else is kept for an adult person
	}
	rows := append([]tmdbDiscoverItem(nil), raw.CombinedCredits.Cast...)
	for _, c := range raw.CombinedCredits.Crew {
		if personCrewJobs[c.Job] {
			rows = append(rows, c.tmdbDiscoverItem)
		}
	}
	type ranked struct {
		item DiscoverItem
		pop  float64
	}
	seen := map[string]int{}
	var list []ranked
	for _, r := range rows {
		// Talk-show and news appearances are dropped (dropNoise): they aren't their work.
		di, ok := r.toItem("", personMinVotes, true)
		if !ok {
			continue
		}
		k := di.MediaType + ":" + strconv.Itoa(di.TMDBID)
		if i, dup := seen[k]; dup {
			list[i].pop = max(list[i].pop, r.Popularity)
			continue
		}
		di.Genres = genreLabels(r.GenreIDs, genres)
		seen[k] = len(list)
		list = append(list, ranked{item: di, pop: r.Popularity})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].pop > list[j].pop })
	for _, r := range list {
		if len(p.Credits) >= personMaxCredits {
			break
		}
		p.Credits = append(p.Credits, r.item)
	}
	return p, nil
}
