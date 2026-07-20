package applog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"
)

// Persistence writes every captured entry to disk as JSON Lines so the Logs page still
// has history after a restart — which is exactly when you most want it, since a crash or
// an update is usually what sent you looking.
//
// Format is one JSON object per line: greppable with standard tools on the server, and
// trivially resumable (a torn final line is skipped on load rather than poisoning the
// file). The file is the long-term record; the ring is the serving window, and is
// re-seeded from the newest lines on startup.

const (
	// FileMaxBytes rotates the active file once it passes this size.
	FileMaxBytes = 24 << 20 // 24 MB
	// FilesKept is how many rotated files to retain alongside the active one.
	FilesKept = 3

	// flushInterval bounds how much is lost to a hard kill. Logging must never block on
	// disk, so writes are buffered and flushed on a ticker instead of per line.
	flushInterval = time.Second
	// queueDepth absorbs bursts (a library scan logs thousands of lines in seconds)
	// without making the logging call wait for the writer.
	queueDepth = 4096
)

// writer serializes entries to the active file and rotates it.
type writer struct {
	path    string
	f       *os.File
	bw      *bufio.Writer
	size    int64
	ch      chan Entry
	done    chan struct{}
	dropped atomic.Int64
}

// Persist starts writing entries added to the ring into path. It returns a stop function
// that drains and flushes; call it during shutdown so the final lines aren't lost.
//
// A failure to open the file is returned rather than swallowed, but a failure to WRITE
// later is not fatal: losing log persistence must never take the app down with it.
func (r *Ring) Persist(path string) (stop func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	info, _ := f.Stat()
	w := &writer{
		path: path, f: f, bw: bufio.NewWriterSize(f, 64<<10),
		ch: make(chan Entry, queueDepth), done: make(chan struct{}),
	}
	if info != nil {
		w.size = info.Size()
	}
	go w.run()

	r.mu.Lock()
	r.sink = w
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		r.sink = nil
		r.mu.Unlock()
		close(w.ch)
		<-w.done
	}, nil
}

// Dropped reports how many entries were discarded because the writer couldn't keep up.
// Surfaced so a silently truncated log is at least countable.
func (r *Ring) Dropped() int64 {
	r.mu.RLock()
	w := r.sink
	r.mu.RUnlock()
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// offer queues an entry without ever blocking the caller. A full queue means the disk
// can't keep up with the log rate; dropping is the only option that doesn't stall the
// application to write its own logs.
func (w *writer) offer(e Entry) {
	select {
	case w.ch <- e:
	default:
		w.dropped.Add(1)
	}
}

func (w *writer) run() {
	defer close(w.done)
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				w.flush()
				_ = w.f.Close()
				return
			}
			w.write(e)
		case <-tick.C:
			w.flush()
		}
	}
}

func (w *writer) write(e Entry) {
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')
	n, err := w.bw.Write(line)
	if err != nil {
		return // disk trouble: drop the line rather than kill the app
	}
	w.size += int64(n)
	if w.size >= FileMaxBytes {
		w.rotate()
	}
}

func (w *writer) flush() {
	if err := w.bw.Flush(); err == nil {
		_ = w.f.Sync()
	}
}

// rotate closes the active file, shifts the numbered history along, and starts fresh.
// Any failure leaves the current file in place and simply keeps appending — an oversized
// log is much better than a lost one.
func (w *writer) rotate() {
	w.flush()
	if err := w.f.Close(); err != nil {
		return
	}
	// Oldest first, so nothing is overwritten before it's shifted.
	_ = os.Remove(rotatedName(w.path, FilesKept))
	for i := FilesKept - 1; i >= 1; i-- {
		_ = os.Rename(rotatedName(w.path, i), rotatedName(w.path, i+1))
	}
	_ = os.Rename(w.path, rotatedName(w.path, 1))

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Couldn't reopen: reattach to whatever we can so logging continues.
		if f, err = os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			return
		}
	}
	w.f, w.size = f, 0
	w.bw.Reset(f)
}

func rotatedName(path string, n int) string { return fmt.Sprintf("%s.%d", path, n) }

// Restore seeds the ring from the persisted files, newest entries last, so the Logs page
// picks up where it left off after a restart. Reads the rotated files oldest-first so
// chronological order is preserved across a rotation boundary.
//
// Malformed lines are skipped: a process killed mid-write leaves a torn final line, and
// that must not cost you the rest of the file.
func (r *Ring) Restore(path string) int {
	files := []string{}
	for i := FilesKept; i >= 1; i-- {
		if name := rotatedName(path, i); fileExists(name) {
			files = append(files, name)
		}
	}
	if fileExists(path) {
		files = append(files, path)
	}

	// Only the newest r.max entries can survive in the ring, so read the tail of each
	// file into one buffer and keep the last window.
	var all []Entry
	for _, name := range files {
		all = append(all, readEntries(name, r.max)...)
	}
	if len(all) > r.max {
		all = all[len(all)-r.max:]
	}
	// Defensive: a clock change or an out-of-order rotation shouldn't scramble the view.
	sort.SliceStable(all, func(i, j int) bool { return all[i].TimeMS < all[j].TimeMS })
	for _, e := range all {
		r.add(e)
	}
	return len(all)
}

// readEntries returns up to the last max entries of a JSON Lines file.
func readEntries(name string, max int) []Entry {
	f, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()

	out := make([]Entry, 0, 1024)
	sc := bufio.NewScanner(f)
	// Attrs can be long (a full ffmpeg command line), so allow well past the 64KB default
	// rather than silently truncating the record.
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.TimeMS == 0 {
			continue // torn or malformed line — skip it, keep the rest
		}
		out = append(out, e)
		if len(out) > max*2 { // bound memory on a large file; trim to the tail
			out = append(out[:0], out[len(out)-max:]...)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

func fileExists(name string) bool {
	st, err := os.Stat(name)
	return err == nil && !st.IsDir()
}
