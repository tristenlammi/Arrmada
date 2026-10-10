package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// peopleDiscovery answers Person from a fixed table; an id it doesn't know is not found.
type peopleDiscovery struct {
	metadata.DiscoveryProvider
	people map[int]*metadata.Person
}

func (peopleDiscovery) Available() bool { return true }
func (p peopleDiscovery) Person(_ context.Context, id int) (*metadata.Person, error) {
	if pp, ok := p.people[id]; ok {
		cp := *pp
		return &cp, nil
	}
	return nil, metadata.ErrNotFound
}

func fetchPerson(a *api, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/discover/person/"+id, nil)
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	a.handleDiscoverPerson(rec, r)
	return rec
}

// A person's page carries their credits as cards with badges; an adult person (however
// the provider answers) and an unknown one are 404.
func TestDiscoverPersonEndpoint(t *testing.T) {
	snap := emptySnap()
	snap.movIn[1], snap.movHave[1] = true, true
	a := &api{deps: Deps{Discovery: peopleDiscovery{people: map[int]*metadata.Person{
		5: {ID: 5, Name: "Ada Mariner", Credits: []metadata.DiscoverItem{{MediaType: "movie", TMDBID: 1, Title: "Harbour Lights"}}},
		6: {ID: 6, Name: "Someone", Adult: true},
	}}}}
	enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: snap})
	t.Cleanup(func() { enrichSnaps.Delete(a) })

	rec := fetchPerson(a, "5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Name    string         `json:"name"`
		Credits []discoverCard `json:"credits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "Ada Mariner" || len(body.Credits) != 1 || !body.Credits[0].HasFile {
		t.Errorf("body = %s", rec.Body)
	}
	for _, id := range []string{"6", "7"} {
		if rec := fetchPerson(a, id); rec.Code != http.StatusNotFound {
			t.Errorf("person %s: status %d, want 404", id, rec.Code)
		}
	}
	if rec := fetchPerson(a, "x"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: status %d, want 400", rec.Code)
	}
}
