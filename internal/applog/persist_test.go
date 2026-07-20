package applog

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point: a restart must not lose the log. This is exactly when you go looking
// at it, since an update or a crash is usually what sent you there.
func TestPersistSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "arrmada.log.jsonl")

	first := NewRing(100)
	stop, err := first.Persist(path)
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), first))
	log.Info("series: grabbing", "series", "Taskmaster")
	log.Warn("series: grab failed", "series", "Taskmaster")
	stop() // drains and flushes, as shutdown does

	// A fresh process: new ring, same file.
	second := NewRing(100)
	if n := second.Restore(path); n != 2 {
		t.Fatalf("restored %d entries, want 2", n)
	}
	got := second.Snapshot(Filter{Min: slog.LevelDebug})
	if len(got) != 2 || got[0].Message != "series: grabbing" || got[1].Level != "WARN" {
		t.Errorf("restored entries = %+v", got)
	}
	if !strings.Contains(got[0].Attrs, "series=Taskmaster") {
		t.Errorf("attrs lost in the round trip: %q", got[0].Attrs)
	}
}

// A process killed mid-write leaves a torn final line. That must cost you that one line,
// not the rest of the file.
func TestRestoreSkipsTornLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arrmada.log.jsonl")
	content := `{"time_ms":1,"level":"INFO","msg":"first"}
not json at all
{"time_ms":2,"level":"INFO","msg":"second"}
{"time_ms":3,"level":"INFO","msg":"tor` // killed mid-write
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRing(100)
	if n := r.Restore(path); n != 2 {
		t.Fatalf("restored %d, want the 2 intact lines", n)
	}
	got := r.Snapshot(Filter{Min: slog.LevelDebug})
	if got[0].Message != "first" || got[1].Message != "second" {
		t.Errorf("wrong entries survived: %+v", got)
	}
}

// Restore reads rotated files oldest-first so chronological order holds across a
// rotation boundary — otherwise the page shows yesterday's lines after today's.
func TestRestoreOrdersAcrossRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arrmada.log.jsonl")
	write := func(name, body string) {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(rotatedName(path, 2), `{"time_ms":10,"level":"INFO","msg":"oldest"}`+"\n")
	write(rotatedName(path, 1), `{"time_ms":20,"level":"INFO","msg":"middle"}`+"\n")
	write(path, `{"time_ms":30,"level":"INFO","msg":"newest"}`+"\n")

	r := NewRing(100)
	if n := r.Restore(path); n != 3 {
		t.Fatalf("restored %d, want 3", n)
	}
	got := r.Snapshot(Filter{Min: slog.LevelDebug})
	for i, want := range []string{"oldest", "middle", "newest"} {
		if got[i].Message != want {
			t.Errorf("position %d = %q, want %q (full: %+v)", i, got[i].Message, want, got)
		}
	}
}

// Restoring more than the ring holds must keep the NEWEST lines, not the first ones read.
func TestRestoreKeepsNewestWhenOverCapacity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arrmada.log.jsonl")
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString(`{"time_ms":`)
		sb.WriteString(itoa(i))
		sb.WriteString(`,"level":"INFO","msg":"line`)
		sb.WriteString(itoa(i))
		sb.WriteString(`"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRing(100) // min capacity
	r.Restore(path)
	got := r.Snapshot(Filter{Min: slog.LevelDebug})
	if len(got) != 100 {
		t.Fatalf("got %d entries, want the ring's 100", len(got))
	}
	if got[len(got)-1].Message != "line200" {
		t.Errorf("newest line = %q, want line200", got[len(got)-1].Message)
	}
	if got[0].Message != "line101" {
		t.Errorf("oldest kept = %q, want line101", got[0].Message)
	}
}

// Restoring from a directory with no log files yet is the first-run case and must be
// silent, not an error or a panic.
func TestRestoreWithNoFiles(t *testing.T) {
	r := NewRing(100)
	if n := r.Restore(filepath.Join(t.TempDir(), "nothing.jsonl")); n != 0 {
		t.Errorf("restored %d from an empty dir, want 0", n)
	}
}

// Persist must create its directory — on a fresh install <DataDir>/logs doesn't exist.
func TestPersistCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "arrmada.log.jsonl")
	stop, err := NewRing(100).Persist(path)
	if err != nil {
		t.Fatalf("persist into a missing dir: %v", err)
	}
	stop()
	if !fileExists(path) {
		t.Error("log file was not created")
	}
}

// Logging must never block on the disk. Once the queue is full, entries are dropped and
// counted rather than stalling the application to write its own logs.
func TestWriterDropsRatherThanBlocks(t *testing.T) {
	w := &writer{ch: make(chan Entry, 2)} // no reader draining it
	for i := 0; i < 10; i++ {
		w.offer(Entry{TimeMS: int64(i), Message: "x"})
	}
	if got := w.dropped.Load(); got != 8 {
		t.Errorf("dropped = %d, want 8 (10 offered, 2 queued)", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Rotation shuffles the numbered history along and drops the oldest. Getting the order
// wrong overwrites a file before it's been shifted, silently losing a day of history.
func TestRotateShiftsHistoryAndDropsOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arrmada.log.jsonl")

	// Seed the active file plus a full set of rotated ones, each identifiable.
	if err := os.WriteFile(path, []byte("active\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= FilesKept; i++ {
		if err := os.WriteFile(rotatedName(path, i), []byte("gen"+itoa(i)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	w := &writer{path: path, f: f, bw: newBufWriter(f)}
	w.rotate()
	defer w.f.Close()

	// The active file became .1, each generation aged by one, and the former oldest is gone.
	if got := readFile(t, rotatedName(path, 1)); got != "active\n" {
		t.Errorf(".1 = %q, want the previously active file", got)
	}
	for i := 1; i < FilesKept; i++ {
		if got := readFile(t, rotatedName(path, i+1)); got != "gen"+itoa(i)+"\n" {
			t.Errorf(".%d = %q, want gen%d", i+1, got, i)
		}
	}
	// A fresh, empty active file is in place and writable.
	if got := readFile(t, path); got != "" {
		t.Errorf("new active file = %q, want empty", got)
	}
	if w.size != 0 {
		t.Errorf("size = %d after rotate, want 0", w.size)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func newBufWriter(f *os.File) *bufio.Writer { return bufio.NewWriter(f) }
