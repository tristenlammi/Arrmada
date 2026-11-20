package audioserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// clipCache holds the chapter tracks cut from very long audiobook files (chaptertracks.go).
// A track is cut the first time it's asked for — a stream copy, about a second for a
// chapter — and kept on disk so an app's Range requests and its next visit are served
// like any file. The originals are only ever read.
//
// The cache is bounded twice: a track nobody has asked for in clipMaxAge is removed, and
// past clipMaxBytes the least recently used go first. Someone listening through a book
// keeps a few chapters here; an app downloading a whole book fills it with that book for
// a few days.
//
// It lives in Arrmada's data folder, beside the database, which on many servers is a small
// disk. So it never takes that disk's last clipFreeFloor: short of room, the tracks asked
// for longest ago go first, and if that isn't enough the track isn't cut at all.
type clipCache struct {
	dir    string
	ffmpeg string
	log    *slog.Logger
	now    func() time.Time
	// cut writes the stretch [start, start+dur) of src to dst as an MP4 (nil = ffmpeg).
	cut func(ctx context.Context, src string, start, dur float64, title, dst string) error
	// free reports the free space on the disk the cache is on (nil = ask the system).
	free func() (uint64, bool)

	maxBytes int64
	maxAge   time.Duration

	mu        sync.Mutex
	making    map[string]*clipMake
	lastSweep time.Time
	slots     chan struct{} // cuts running at once
}

type clipMake struct {
	done chan struct{}
	err  error
}

const (
	clipMaxBytes = 8 << 30
	clipMaxAge   = 7 * 24 * time.Hour
	// clipBusy is how recently a track must have been asked for to be safe from the size
	// cap: an app is probably still reading it.
	clipBusy = 15 * time.Minute
	// clipCutTimeout bounds one cut. Copying a chapter takes about a second; a sleeping
	// array disk adds its spin-up.
	clipCutTimeout = 3 * time.Minute
	clipSweepEvery = 10 * time.Minute
	// clipFreeFloor is the free space the cache always leaves on its disk.
	clipFreeFloor = 1 << 30
)

var errClipNoRoom = errors.New("not enough free space in Arrmada's data folder to prepare a chapter (it keeps 1 GB free)")

// freeOf is the free space on the cache's disk, when that can be told.
func (c *clipCache) freeOf() (uint64, bool) {
	if c.free != nil {
		return c.free()
	}
	u, ok := diskspace.Of(c.dir)
	return u.FreeBytes, ok
}

// room reports whether a cut of about size bytes fits above the floor.
func (c *clipCache) room(size int64) bool {
	free, ok := c.freeOf()
	return !ok || free >= uint64(clipFreeFloor)+uint64(max(size, 0))
}

func newClipCache(dir, ffmpeg string, log *slog.Logger) *clipCache {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if log == nil {
		log = slog.Default()
	}
	return &clipCache{dir: dir, ffmpeg: ffmpeg, log: log, maxBytes: clipMaxBytes, maxAge: clipMaxAge,
		making: map[string]*clipMake{}, slots: make(chan struct{}, 2)}
}

func (c *clipCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// pathFor is where a track's cut lives. The name is made from the source file as it is now
// (path, size, time) and the stretch, so a replaced file never serves an old cut.
func (c *clipCache) pathFor(f AudioFile) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d", f.Path, f.Clip.SrcSize, f.MTimeMs,
		int64(f.Clip.Start*1000), int64(f.Clip.End*1000))))
	return filepath.Join(c.dir, hex.EncodeToString(h[:16])+".m4a")
}

// ready reports whether a track's cut is already on disk.
func (c *clipCache) ready(f AudioFile) bool {
	st, err := os.Stat(c.pathFor(f))
	return err == nil && st.Size() > 0
}

// get returns the path of a track's cut, making it first if needed. One cut per track
// runs at a time and everyone asking meanwhile waits for it; the cut itself doesn't stop
// when the caller that started it goes away, since another may be waiting on it.
func (c *clipCache) get(ctx context.Context, f AudioFile) (string, error) {
	if f.Clip == nil {
		return "", errors.New("not a chapter track")
	}
	dst := c.pathFor(f)
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		now := c.clock()
		_ = os.Chtimes(dst, now, now) // last asked for: what the sweep goes by
		return dst, nil
	}
	m := c.start(f, dst)
	select {
	case <-m.done:
		if m.err != nil {
			return "", m.err
		}
		return dst, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// begin starts cutting a track that isn't on disk yet, without waiting for it.
func (c *clipCache) begin(f AudioFile) {
	if f.Clip != nil && !c.ready(f) {
		c.start(f, c.pathFor(f))
	}
}

// start returns the cut of dst in progress, starting it if none is.
func (c *clipCache) start(f AudioFile, dst string) *clipMake {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, running := c.making[dst]; running {
		return m
	}
	m := &clipMake{done: make(chan struct{})}
	// A cut that finished between the caller's look at the disk and now is the answer:
	// without this second look, under the lock, it would be cut again.
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		close(m.done)
		return m
	}
	c.making[dst] = m
	safego.Go(c.log, "audiobook chapter track", func() {
		m.err = c.make(f, dst)
		c.mu.Lock()
		delete(c.making, dst)
		c.mu.Unlock()
		close(m.done)
		c.sweepSometimes()
	})
	return m
}

func (c *clipCache) make(f AudioFile, dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipCutTimeout)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	if !c.room(f.Size) {
		c.sweepFor(f.Size)
		if !c.room(f.Size) {
			return errClipNoRoom
		}
	}
	tmp, err := os.CreateTemp(c.dir, "cut-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	cut := c.cut
	if cut == nil {
		cut = c.ffmpegCut
	}
	dur := f.Clip.End - f.Clip.Start
	if f.Clip.Last {
		dur = 0 // to the end of the file, whatever its last frame's time
	}
	if err := cut(ctx, f.Path, f.Clip.Start, dur, f.Title, tmpPath); err != nil {
		return err
	}
	if st, err := os.Stat(tmpPath); err != nil || st.Size() == 0 {
		return errors.New("the chapter came out empty")
	}
	return os.Rename(tmpPath, dst)
}

// ffmpegCut copies one stretch of src's audio into a new MP4 with its index at the front.
// Nothing is re-encoded, so the cut lands on the audio frame at or just before start
// (within 1/40 s) and costs about as long as reading that much of the file.
func (c *clipCache) ffmpegCut(ctx context.Context, src string, start, dur float64, title, dst string) error {
	args := []string{"-nostdin", "-v", "error", "-y"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", src)
	if dur > 0 {
		args = append(args, "-t", strconv.FormatFloat(dur, 'f', 3, 64))
	}
	args = append(args, "-map", "0:a:0", "-c", "copy", "-map_metadata", "-1", "-map_chapters", "-1",
		"-metadata", "title="+title, "-movflags", "+faststart", "-f", "mp4", dst)
	out, err := toolrun.Command(ctx, toolrun.Normal, c.ffmpeg, args...).CombinedOutput()
	if err != nil {
		// ffmpeg names the file in its errors; the log line mustn't (it would say what
		// someone is listening to).
		msg := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(out), src, "the file"), dst, "the track"))
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, clip(msg, 200))
	}
	return nil
}

func (c *clipCache) sweepSometimes() {
	c.mu.Lock()
	now := c.clock()
	due := now.Sub(c.lastSweep) >= clipSweepEvery
	if due {
		c.lastSweep = now
	}
	c.mu.Unlock()
	if due {
		c.sweep()
	}
}

// sweep removes tracks nobody has asked for in maxAge, then the least recently asked for
// until the cache is under maxBytes (leaving alone ones asked for in the last clipBusy),
// and any half-written cut a crash left behind. It returns how many files it removed.
func (c *clipCache) sweep() int { return c.sweepFor(0) }

// sweepFor is sweep, also making room for a cut of need bytes above clipFreeFloor when the
// disk is short of it.
func (c *clipCache) sweepFor(need int64) int {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	now := c.clock()
	type cut struct {
		path string
		size int64
		at   time.Time
	}
	var kept []cut
	var total int64
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		p := filepath.Join(c.dir, e.Name())
		age := now.Sub(info.ModTime())
		switch {
		case strings.HasSuffix(e.Name(), ".tmp"):
			if age > 2*clipCutTimeout && os.Remove(p) == nil {
				removed++
			}
		case !strings.HasSuffix(e.Name(), ".m4a"):
		case age > c.maxAge:
			if os.Remove(p) == nil {
				removed++
			}
		default:
			kept = append(kept, cut{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
	}
	limit := c.maxBytes
	if free, ok := c.freeOf(); ok {
		if want := uint64(clipFreeFloor) + uint64(max(need, 0)); free < want {
			limit = min(limit, total-int64(want-free))
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].at.Before(kept[j].at) })
	for _, k := range kept {
		if total <= limit || now.Sub(k.at) < clipBusy {
			break
		}
		if os.Remove(k.path) == nil {
			total -= k.size
			removed++
		}
	}
	return removed
}
