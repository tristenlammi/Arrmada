package books

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// A book's details can be edited by hand, field by field, with what the catalogue matched
// kept underneath each edited field (migration 0334_book_edits).
//
// The book's columns hold what is SHOWN: the edit when a field has one, else the
// catalogue's value. So everything that reads a book — its page, the library and ⌘K
// search, the audiobook server, OPDS, file naming, series grouping, requests — uses the
// edit without knowing edits exist. edits_json records which fields are edited and the
// catalogue's value under each one, for the "edited" marker, the hint and Reset.
//
// Catalogue writes (a refresh, a re-match, the Hardcover upgrade, a series learned from a
// release, a merge) go through setMatched, which puts a new catalogue value underneath an
// edited field instead of over it. A write that doesn't (one added later, or a plain
// UPDATE) is caught by the migration's triggers, which do the same. Only an edit — a
// write that also moves edited_at — changes an edited field's column.

// The fields that can be edited, by the name the API, the page and edits_json use.
const (
	FieldTitle          = "title"
	FieldSubtitle       = "subtitle"
	FieldAuthor         = "author"
	FieldSeriesName     = "series_name"
	FieldSeriesPosition = "series_position"
	FieldDescription    = "description"
	FieldYear           = "year"
	FieldLanguage       = "language"
	FieldSubjects       = "subjects"
	FieldISBN           = "isbn"
	FieldNarrator       = "narrator"
	FieldPublisher      = "publisher"
)

// Limits on what can be typed, in characters (subjects: in tags).
const (
	maxEditTitle       = 300
	maxEditName        = 300 // author(s): several names fit
	maxEditSeries      = 200
	maxEditDescription = 20000
	maxEditPublisher   = 200
	maxEditSubject     = 100
	maxEditSubjects    = 50
	maxSeriesPosition  = 100000
)

// ErrEditInvalid is an edit that can't be saved; the message says why, in words the page
// can show.
var ErrEditInvalid = errors.New("invalid edit")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrEditInvalid}, args...)...)
}

// Edits are a book's hand-edited fields, each with the catalogue's value underneath (as
// JSON: a string, a number, or a list for subjects). A field not in it isn't edited.
type Edits map[string]json.RawMessage

// editField is one editable field: its column, and how a value moves between the
// column, the Book and JSON.
type editField struct {
	name   string
	column string
	// clean validates a value typed by hand and returns it in its stored form.
	clean func(raw json.RawMessage) (json.RawMessage, error)
	// get reads the field's shown value from a Book, as JSON.
	get func(b *Book) json.RawMessage
	// put sets the field on a Book from JSON (a catalogue value, for Matched).
	put func(b *Book, raw json.RawMessage)
}

// arg is the value to write to the field's column.
func (f editField) arg(raw json.RawMessage) any {
	switch f.name {
	case FieldYear:
		var n int
		_ = json.Unmarshal(raw, &n)
		return n
	case FieldSeriesPosition:
		var n float64
		_ = json.Unmarshal(raw, &n)
		return n
	case FieldSubjects:
		var list []string
		_ = json.Unmarshal(raw, &list)
		return subjectsJSON(list)
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func jsonOf(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}

func strField(name, column string, clean func(string) (string, error), ptr func(b *Book) *string) editField {
	return editField{
		name: name, column: column,
		clean: func(raw json.RawMessage) (json.RawMessage, error) {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, invalid("%s must be text", fieldLabel(name))
			}
			out, err := clean(s)
			if err != nil {
				return nil, err
			}
			return jsonOf(out), nil
		},
		get: func(b *Book) json.RawMessage { return jsonOf(*ptr(b)) },
		put: func(b *Book, raw json.RawMessage) {
			var s string
			_ = json.Unmarshal(raw, &s)
			*ptr(b) = s
		},
	}
}

// text is a single line of text typed by hand, at most max characters, with no control
// or bidi characters (displaytitle's rules, the ones a display title is held to).
func text(name string, max int, required bool) func(string) (string, error) {
	return func(s string) (string, error) {
		out, ok := displaytitle.CleanText(strings.Join(strings.Fields(s), " "), max, false)
		if !ok {
			return "", invalid("%s must be one line of at most %d characters, with no control characters", fieldLabel(name), max)
		}
		if required && out == "" {
			return "", invalid("%s can't be empty", fieldLabel(name))
		}
		return out, nil
	}
}

// editFields is every editable field, in the order the page lists them.
var editFields = []editField{
	strField(FieldTitle, "title", text(FieldTitle, maxEditTitle, true), func(b *Book) *string { return &b.Title }),
	strField(FieldSubtitle, "subtitle", text(FieldSubtitle, maxEditTitle, false), func(b *Book) *string { return &b.Subtitle }),
	strField(FieldAuthor, "author", text(FieldAuthor, maxEditName, false), func(b *Book) *string { return &b.Author }),
	strField(FieldSeriesName, "series_name", text(FieldSeriesName, maxEditSeries, false), func(b *Book) *string { return &b.SeriesName }),
	{
		name: FieldSeriesPosition, column: "series_position",
		clean: cleanSeriesPosition,
		get:   func(b *Book) json.RawMessage { return jsonOf(b.SeriesPosition) },
		put: func(b *Book, raw json.RawMessage) {
			_ = json.Unmarshal(raw, &b.SeriesPosition)
		},
	},
	strField(FieldDescription, "description", func(s string) (string, error) {
		out, ok := displaytitle.CleanText(s, maxEditDescription, true)
		if !ok {
			return "", invalid("the description must be at most %d characters, with no control characters", maxEditDescription)
		}
		return out, nil
	}, func(b *Book) *string { return &b.Description }),
	{
		name: FieldYear, column: "year",
		clean: cleanYear,
		get:   func(b *Book) json.RawMessage { return jsonOf(b.Year) },
		put:   func(b *Book, raw json.RawMessage) { _ = json.Unmarshal(raw, &b.Year) },
	},
	strField(FieldLanguage, "language", cleanLanguage, func(b *Book) *string { return &b.Language }),
	{
		name: FieldSubjects, column: "subjects_json",
		clean: cleanSubjects,
		get: func(b *Book) json.RawMessage {
			if len(b.Subjects) == 0 {
				return json.RawMessage(`[]`)
			}
			return jsonOf(b.Subjects)
		},
		put: func(b *Book, raw json.RawMessage) {
			b.Subjects = nil
			_ = json.Unmarshal(raw, &b.Subjects)
			if len(b.Subjects) == 0 {
				b.Subjects = nil
			}
		},
	},
	strField(FieldISBN, "isbn", CleanISBN, func(b *Book) *string { return &b.ISBN }),
	strField(FieldNarrator, "audiobook_narrator", func(s string) (string, error) {
		if _, err := CleanNarrator(s); err != nil {
			return "", invalid("keep the narrator under 200 characters")
		}
		return text(FieldNarrator, 200, false)(s)
	}, func(b *Book) *string { return &b.AudiobookNarrator }),
	strField(FieldPublisher, "publisher", text(FieldPublisher, maxEditPublisher, false), func(b *Book) *string { return &b.Publisher }),
}

func fieldByName(name string) (editField, bool) {
	for _, f := range editFields {
		if f.name == name {
			return f, true
		}
	}
	return editField{}, false
}

// EditableFields lists the fields that can be edited, in the page's order.
func EditableFields() []string {
	out := make([]string, 0, len(editFields))
	for _, f := range editFields {
		out = append(out, f.name)
	}
	return out
}

// fieldLabel is a field's name in a sentence.
func fieldLabel(name string) string {
	switch name {
	case FieldSeriesName:
		return "the series"
	case FieldSeriesPosition:
		return "the series position"
	case FieldISBN:
		return "the ISBN"
	case FieldSubjects:
		return "the tags"
	case FieldYear:
		return "the year"
	}
	return "the " + name
}

// cleanSeriesPosition takes a number (or a number typed as text: "1.5"), 0 meaning "in
// the series, number unknown". Up to three decimals, so a novella reads #1.5, not
// #1.4999999.
func cleanSeriesPosition(raw json.RawMessage) (json.RawMessage, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, invalid("the series position must be a number, like 2 or 1.5")
		}
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
		if s == "" {
			return jsonOf(0), nil
		}
		v, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return nil, invalid("the series position must be a number, like 2 or 1.5")
		}
		n = v
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > maxSeriesPosition {
		return nil, invalid("the series position must be a number from 0 to %d", maxSeriesPosition)
	}
	return jsonOf(math.Round(n*1000) / 1000), nil
}

// cleanYear takes a year (0 = not known), as a number or text.
func cleanYear(raw json.RawMessage) (json.RawMessage, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, invalid("the year must be a whole number")
		}
		if s = strings.TrimSpace(s); s == "" {
			return jsonOf(0), nil
		}
		v, perr := strconv.Atoi(s)
		if perr != nil {
			return nil, invalid("the year must be a whole number")
		}
		n = v
	}
	if n != 0 && (n < 1000 || n > time.Now().Year()+10) {
		return nil, invalid("the year must be between 1000 and %d (or empty)", time.Now().Year()+10)
	}
	return jsonOf(n), nil
}

// cleanLanguage takes any spelling of a language internal/langs knows ("French", "fre",
// "fr") and stores its canonical code; "" clears it.
func cleanLanguage(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	l, ok := langs.Lookup(s)
	if !ok {
		return "", invalid("%q isn't a language Arrmada knows — pick one from the list", s)
	}
	return l.Code, nil
}

// cleanSubjects takes a list of tags or genres: each one line of at most 100 characters,
// at most 50, repeats (ignoring case) dropped.
func cleanSubjects(raw json.RawMessage) (json.RawMessage, error) {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, invalid("the tags must be a list of words or phrases")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range list {
		t, err := text(FieldSubjects, maxEditSubject, false)(s)
		if err != nil {
			return nil, invalid("each tag must be one line of at most %d characters", maxEditSubject)
		}
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > maxEditSubjects {
		return nil, invalid("keep it to %d tags or fewer", maxEditSubjects)
	}
	return jsonOf(out), nil
}

// CleanISBN checks an ISBN typed by hand — ISBN-10 or ISBN-13, with or without hyphens
// and spaces — against its check digit, and returns it as bare digits. "" clears it.
func CleanISBN(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != 'X' && r != 'x' && r != '-' && r != ' ' {
			return "", invalid("an ISBN is 10 or 13 digits (hyphens and spaces are fine)")
		}
	}
	isbn := metadata.NormalizeISBN(s)
	if isbn == "" {
		return "", invalid("that ISBN's check digit doesn't match — check it for a typo")
	}
	return isbn, nil
}

// Edited reports whether a field of the book was edited by hand.
func (b Book) Edited(field string) bool {
	_, ok := b.Edits[field]
	return ok
}

// Matched is the book as the catalogue has it: every edited field back to the
// catalogue's value. For comparing against a catalogue (a refresh, the Hardcover upgrade)
// and for searching by the catalogue's names too.
func (b Book) Matched() Book {
	if len(b.Edits) == 0 {
		return b
	}
	m := b
	m.Subjects = append([]string(nil), b.Subjects...)
	for name, raw := range b.Edits {
		if f, ok := fieldByName(name); ok {
			f.put(&m, raw)
		}
	}
	m.Edits = nil
	return m
}

// TitleVariant reports whether two titles are spellings of one title rather than two
// titles: their main titles — before any subtitle (":", " - ", " — ", " – ") and without
// a trailing "(…)"/"[…]" note or a leading "The"/"A"/"An" — fold to the same words
// (accents, case, punctuation and apostrophes don't count).
//
//	"The Way of Kings" / "The Way of Kings: The Stormlight Archive, Book 1"  → variant
//	"Enders Game" / "Ender's Game"                                            → variant
//	"Dune" / "Dune Messiah"                                                   → not
//	"…Philosopher's Stone" / "…Sorcerer's Stone"                              → not
//
// The last two are why it matters: an edit that changes the main title is a correction to
// another title, and the old one names some other book.
func TitleVariant(a, b string) bool {
	ka, kb := mainTitleKey(a), mainTitleKey(b)
	return ka != "" && ka == kb
}

func mainTitleKey(t string) string {
	t = stripTrailingNote(strings.TrimSpace(t))
	if i, _ := firstSep(t); i > 0 {
		t = t[:i]
	}
	k := joinedKey(t)
	for _, article := range []string{"the ", "a ", "an "} {
		if rest := strings.TrimPrefix(k, article); rest != k && rest != "" {
			return rest
		}
	}
	return k
}

// CatalogueNames is the book under the names the catalogue gives it, for matching
// releases and searching when the title or author was edited by hand — or ok=false when
// that adds nothing. The catalogue's author is always usable (an author only ranks a
// match, and a search under it is still checked against the title). The catalogue's
// title is used only when it's a TitleVariant of the edited one (a subtitle added or
// removed, a spelling or punctuation fix): a title edited into another title ("Dune" →
// "Dune Messiah") was a correction, and the catalogue's would match the other book —
// which may well be in the library too.
func (b Book) CatalogueNames() (Book, bool) {
	if !b.Edited(FieldTitle) && !b.Edited(FieldAuthor) {
		return b, false
	}
	m := b.Matched()
	out := b
	out.Edits = nil
	if m.Author != b.Author && strings.TrimSpace(m.Author) != "" {
		out.Author = m.Author
	}
	if m.Title != b.Title && TitleVariant(b.Title, m.Title) {
		out.Title = m.Title
	}
	return out, out.Title != b.Title || out.Author != b.Author
}

// EditRequest is one save of the edit dialog: fields to set to a value typed by hand,
// fields to put back to the catalogue's value, or everything put back.
type EditRequest struct {
	Set      map[string]json.RawMessage `json:"set"`
	Reset    []string                   `json:"reset"`
	ResetAll bool                       `json:"reset_all"`
}

// EditResult is what a save changed.
type EditResult struct {
	Book Book
	// Changed are the fields whose shown value changed, in the page's order.
	Changed []string
}

// cleanEdits validates a request and returns the set values in stored form.
func cleanEdits(req EditRequest) (map[string]json.RawMessage, []string, error) {
	set := map[string]json.RawMessage{}
	for name, raw := range req.Set {
		f, ok := fieldByName(name)
		if !ok {
			return nil, nil, invalid("%q isn't a detail that can be edited", name)
		}
		v, err := f.clean(raw)
		if err != nil {
			return nil, nil, err
		}
		set[name] = v
	}
	var reset []string
	for _, name := range req.Reset {
		if _, ok := fieldByName(name); !ok {
			return nil, nil, invalid("%q isn't a detail that can be edited", name)
		}
		if _, both := set[name]; both {
			return nil, nil, invalid("%s is both set and reset", fieldLabel(name))
		}
		reset = append(reset, name)
	}
	return set, reset, nil
}

// EditDetails saves hand edits to a book's details: each set field shows the value typed
// from now on, over whatever the catalogue says, and each reset field goes back to the
// catalogue's value. Setting a field to exactly the catalogue's value is a reset — there's
// nothing left to mark. A changed author re-links the book to the author by that name.
func (s *Service) EditDetails(ctx context.Context, id int64, req EditRequest) (EditResult, error) {
	set, reset, err := cleanEdits(req)
	if err != nil {
		return EditResult{}, err
	}
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return EditResult{}, err
	}
	changed, err := s.repo.applyEdits(ctx, id, set, reset, req.ResetAll)
	if err != nil {
		return EditResult{}, err
	}
	after, err := s.Get(ctx, id)
	if err != nil {
		return EditResult{}, err
	}
	if len(changed) > 0 {
		labels := make([]string, 0, len(changed))
		for _, c := range changed {
			labels = append(labels, strings.TrimPrefix(fieldLabel(c), "the "))
		}
		s.repo.AddEvent(ctx, id, "edited", "Details edited by hand: "+strings.Join(labels, ", "))
	}
	if !strings.EqualFold(strings.TrimSpace(before.Author), strings.TrimSpace(after.Author)) {
		after.AuthorID = s.linkAuthor(ctx, id, after.Author, "", true)
	}
	return EditResult{Book: after, Changed: changed}, nil
}

// applyEdits writes an edit in one transaction: set fields get their new value in the
// column, with the catalogue's value kept in edits_json (the column's value, unless the
// field was already edited); reset fields get the catalogue's value back. edited_at moves,
// which is what lets the migration's triggers tell this write from a catalogue one.
// Returns the fields whose shown value changed.
func (r *Repo) applyEdits(ctx context.Context, id int64, set map[string]json.RawMessage, reset []string, resetAll bool) ([]string, error) {
	var changed []string
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		b, err := getTx(ctx, tx, id)
		if err != nil {
			return err
		}
		edits := Edits{}
		for k, v := range b.Edits {
			edits[k] = v
		}
		next := map[string]json.RawMessage{} // the column values to write
		restore := func(name string) {
			if under, ok := edits[name]; ok {
				next[name] = under
				delete(edits, name)
			}
		}
		if resetAll {
			for name := range b.Edits {
				restore(name)
			}
		}
		for _, name := range reset {
			restore(name)
		}
		for name, v := range set {
			f, _ := fieldByName(name)
			// What's underneath is the catalogue's value from before this save, even when
			// Reset all in the same save already put it back.
			under, wasEdited := b.Edits[name]
			if !wasEdited {
				under = f.get(&b)
			}
			if sameJSON(v, under) {
				// Typed the catalogue's value back: no edit left, and the column holds
				// the catalogue's value.
				delete(edits, name)
				next[name] = under
				continue
			}
			edits[name] = under
			next[name] = v
		}
		var sets []string
		var args []any
		for _, f := range editFields {
			v, ok := next[f.name]
			if !ok {
				continue
			}
			if !sameJSON(v, f.get(&b)) {
				changed = append(changed, f.name)
			}
			sets = append(sets, f.column+" = ?")
			args = append(args, f.arg(v))
		}
		raw, err := encodeEdits(edits)
		if err != nil {
			return err
		}
		sets = append(sets, "edits_json = ?", "edited_at = "+store.ScalarMax(tx, "?", "edited_at + 1"))
		args = append(args, raw, time.Now().UnixMilli(), id)
		_, err = tx.ExecContext(ctx, `UPDATE books SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		return err
	})
	return changed, err
}

// setMatched writes catalogue values for a book's fields (Go values: string, int, float64
// or []string, by field). A field edited by hand keeps its edit and gets the new value
// underneath; any other field gets it in its column. Only what differs is written, so a
// refresh that finds nothing new changes nothing (and doesn't move meta_updated_at).
func setMatched(ctx context.Context, ex store.Execer, id int64, vals map[string]any) error {
	b, err := getTx(ctx, ex, id)
	if err != nil {
		return err
	}
	edits := Edits{}
	for k, v := range b.Edits {
		edits[k] = v
	}
	editsChanged := false
	var sets []string
	var args []any
	for _, f := range editFields {
		v, ok := vals[f.name]
		if !ok {
			continue
		}
		raw := jsonOf(v)
		if f.name == FieldSubjects {
			list, _ := v.([]string)
			if len(list) == 0 {
				raw = json.RawMessage(`[]`)
			}
		}
		if under, edited := edits[f.name]; edited {
			if !sameJSON(under, raw) {
				edits[f.name] = raw
				editsChanged = true
			}
			continue
		}
		if sameJSON(raw, f.get(&b)) {
			continue
		}
		sets = append(sets, f.column+" = ?")
		args = append(args, f.arg(raw))
	}
	if editsChanged {
		enc, err := encodeEdits(edits)
		if err != nil {
			return err
		}
		sets = append(sets, "edits_json = ?")
		args = append(args, enc)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err = ex.ExecContext(ctx, `UPDATE books SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// SetMatched is setMatched in its own transaction.
func (r *Repo) SetMatched(ctx context.Context, id int64, vals map[string]any) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error { return setMatched(ctx, tx, id, vals) })
}

// clearEditsTx puts every edited field of a book back to the catalogue's value — the
// "clear my edits" answer to a re-match onto a different book.
func clearEditsTx(ctx context.Context, tx *store.Transaction, id int64) error {
	b, err := getTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if len(b.Edits) == 0 {
		return nil
	}
	var sets []string
	var args []any
	for _, f := range editFields {
		if under, ok := b.Edits[f.name]; ok {
			sets = append(sets, f.column+" = ?")
			args = append(args, f.arg(under))
		}
	}
	sets = append(sets, "edits_json = '{}'", "edited_at = "+store.ScalarMax(tx, "?", "edited_at + 1"))
	args = append(args, time.Now().UnixMilli(), id)
	_, err = tx.ExecContext(ctx, `UPDATE books SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// encodeEdits is edits_json's value: the edited fields, keys sorted so equal edits are
// equal text.
func encodeEdits(e Edits) (string, error) {
	if len(e) == 0 {
		return "{}", nil
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		var compact bytes.Buffer
		if err := json.Compact(&compact, e[k]); err != nil {
			return "", err
		}
		buf.Write(compact.Bytes())
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

// decodeEdits reads edits_json; anything unreadable reads as no edits (never as an
// error that would hide the book).
func decodeEdits(raw string) Edits {
	if raw == "" || raw == "{}" {
		return nil
	}
	var e Edits
	if err := json.Unmarshal([]byte(raw), &e); err != nil || len(e) == 0 {
		return nil
	}
	for k, v := range e {
		if _, ok := fieldByName(k); !ok || len(v) == 0 || string(v) == "null" {
			delete(e, k)
		}
	}
	if len(e) == 0 {
		return nil
	}
	return e
}

// sameJSON compares two JSON values by meaning (1 and 1.0 are the same number).
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

// getTx is Repo.Get on a transaction (or the pool).
func getTx(ctx context.Context, ex store.Execer, id int64) (Book, error) {
	b, err := scan(ex.QueryRowContext(ctx, `SELECT `+cols+` FROM books WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, ErrNotFound
	}
	return b, err
}
