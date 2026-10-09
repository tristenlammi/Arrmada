package httpapi

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// ACQ-21: the Downloads feed keys titles with the shared parser.TitleKey, so a movie with
// an ampersand shows its progress while the torrent spells the word out.
func TestDownloadForAmpersandTitle(t *testing.T) {
	m := movies.Movie{Title: "Love & Death", Year: 2023}
	queue := []download.Item{
		{Name: "Love.Death.and.Robots.2019.1080p.WEB.x264-GRP", Progress: 0.9, State: "downloading"},
		{Name: "Love.and.Death.2023.1080p.WEB.H264-GRP", Progress: 0.4, State: "downloading"},
	}
	d := downloadFor(queue, m)
	if d == nil {
		t.Fatal("the Love.and.Death torrent wasn't recognised as the movie's download")
	}
	if d.Progress != 0.4 {
		t.Errorf("matched the wrong torrent: progress %v", d.Progress)
	}
	if p, ok := queueProgressByTitle(queue, "Pokémon Heroes", 2002); ok {
		t.Errorf("unrelated title matched with progress %v", p)
	}
	if _, ok := queueProgressByTitle([]download.Item{{Name: "Pokemon.Heroes.2002.1080p", Progress: 0.2}}, "Pokémon Heroes", 2002); !ok {
		t.Error("an accented title must match its ASCII torrent")
	}
}
