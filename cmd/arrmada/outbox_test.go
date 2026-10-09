package main

import (
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/outbox"
)

// Every import topic has the consumers the app relies on. A consumer missing here means
// its rows are never written — the side effect silently stops happening — so the list is
// pinned.
func TestImportConsumersCoverEveryTopic(t *testing.T) {
	box := outbox.New(nil, nil)
	importConsumers{}.register(box) // handlers only close over the services; none run here
	want := []string{
		"book.imported → audioserver.cache",
		"book.imported → requests.ready",
		"movie.changed → convert",
		"movie.changed → subtitles",
		"movie.imported → convert",
		"movie.imported → requests.ready",
		"movie.imported → subtitles",
		"series.imported → convert",
		"series.imported → requests.ready",
		"series.imported → subtitles",
	}
	if got := box.Topics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered consumers:\n got %v\nwant %v", got, want)
	}
}
