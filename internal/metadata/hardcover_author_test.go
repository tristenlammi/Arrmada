package metadata

import (
	"encoding/json"
	"testing"
)

// The author is the contributor whose role says "wrote it", wherever Hardcover lists
// them. Empire of the Vampire lists the illustrator first.
func TestHardcoverAuthorIsTheWriterNotTheFirstContributor(t *testing.T) {
	var b hcBook
	if err := json.Unmarshal([]byte(`{"id":1,"title":"Empire of the Vampire","contributions":[
		{"contribution":"Illustrator","author":{"id":2,"name":"Bon Orthwick"}},
		{"contribution":"","author":{"id":3,"name":"Jay Kristoff"}}]}`), &b); err != nil {
		t.Fatal(err)
	}
	if got := b.result().Author; got != "Jay Kristoff" {
		t.Errorf("book author = %q, want Jay Kristoff", got)
	}
	var d hcSearchDoc
	if err := json.Unmarshal([]byte(`{"id":1,"title":"Empire of the Vampire","author_names":["Bon Orthwick","Jay Kristoff"],
		"contributions":[{"contribution":"Illustrator","author":{"name":"Bon Orthwick"}},{"contribution":null,"author":{"name":"Jay Kristoff"}}]}`), &d); err != nil {
		t.Fatal(err)
	}
	if r, _ := d.bookResult(); r.Author != "Jay Kristoff" {
		t.Errorf("search author = %q, want Jay Kristoff", r.Author)
	}
	// No roles at all (an index shape this code doesn't know): the first name, as before.
	var plain hcSearchDoc
	_ = json.Unmarshal([]byte(`{"id":1,"title":"Dune","author_names":["Frank Herbert"],"contributions":"weird"}`), &plain)
	if r, _ := plain.bookResult(); r.Author != "Frank Herbert" {
		t.Errorf("no usable roles: author = %q, want Frank Herbert", r.Author)
	}
}
