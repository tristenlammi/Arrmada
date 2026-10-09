package libroots

import (
	"context"
	"strings"

	"github.com/tristenlammi/arrmada/internal/config"
)

// Settings keys for the folders chosen in the app (first-run setup, Settings → Library).
// Each falls back to the environment's folder (config.*Dir) while unset or blank.
const (
	KeyMovies     = "lib_movies_dir"
	KeyTV         = "lib_tv_dir"
	KeyEbooks     = "lib_ebooks_dir"
	KeyAudiobooks = "lib_audiobooks_dir"
	KeyMusic      = "lib_music_dir"
	KeyDownloads  = "lib_downloads_dir"
)

// Getter reads one setting (settings.Service.Get).
type Getter func(ctx context.Context, key, def string) string

// Roots resolves each library folder and the downloads folder from the saved settings
// every time it's asked, so a folder changed in the app reaches the importer, the
// download client's save path and the disk guard on their next use, with no restart.
//
// There is no cache on purpose: a setting read is one map lookup in memory, folders are
// resolved a handful of times per import or minute, and a cache would only add a window
// in which two parts of the app disagree about where the library is.
type Roots struct {
	get Getter
	cfg config.Config
}

// New builds the resolver. cfg must be the environment's config as loaded (never one
// already overlaid with saved folders): its folders are the defaults a blank setting
// falls back to.
func New(get Getter, cfg config.Config) *Roots { return &Roots{get: get, cfg: cfg} }

// Root is one named folder ("movies", "downloads", …) and where it resolves to now.
type Root struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// resolve is the saved folder, or def when nothing (or only whitespace) is saved.
// settings.Get hands back a stored "" as-is, so the blank check can't be left to it.
func (r *Roots) resolve(ctx context.Context, key, def string) string {
	if r == nil {
		return strings.TrimSpace(def)
	}
	if r.get != nil {
		if v := strings.TrimSpace(r.get(ctx, key, "")); v != "" {
			return v
		}
	}
	return strings.TrimSpace(def)
}

// The resolved folder for each library, and for downloads.
func (r *Roots) Movies(ctx context.Context) string {
	return r.resolve(ctx, KeyMovies, r.env().MoviesDir)
}
func (r *Roots) TV(ctx context.Context) string { return r.resolve(ctx, KeyTV, r.env().TVDir) }
func (r *Roots) Ebooks(ctx context.Context) string {
	return r.resolve(ctx, KeyEbooks, r.env().EbooksDir)
}
func (r *Roots) Audiobooks(ctx context.Context) string {
	return r.resolve(ctx, KeyAudiobooks, r.env().AudiobooksDir)
}
func (r *Roots) Music(ctx context.Context) string { return r.resolve(ctx, KeyMusic, r.env().MusicDir) }
func (r *Roots) Downloads(ctx context.Context) string {
	return r.resolve(ctx, KeyDownloads, r.env().DownloadsDir)
}

// env is the environment's config (zero for a nil resolver).
func (r *Roots) env() config.Config {
	if r == nil {
		return config.Config{}
	}
	return r.cfg
}

// Libraries lists the five library folders (movies, tv, ebooks, audiobooks, music), in
// that order, skipping any that resolve to nothing. Downloads isn't a library.
func (r *Roots) Libraries(ctx context.Context) []Root {
	var out []Root
	for _, x := range []Root{
		{"movies", r.Movies(ctx)},
		{"tv", r.TV(ctx)},
		{"ebooks", r.Ebooks(ctx)},
		{"audiobooks", r.Audiobooks(ctx)},
		{"music", r.Music(ctx)},
	} {
		if x.Path != "" {
			out = append(out, x)
		}
	}
	return out
}

// All is Libraries plus downloads, last.
func (r *Roots) All(ctx context.Context) []Root {
	out := r.Libraries(ctx)
	if d := r.Downloads(ctx); d != "" {
		out = append(out, Root{"downloads", d})
	}
	return out
}

// Config is the environment's config with every folder replaced by the resolved one,
// for code that judges folders through a config.Config (the health panel's folder list,
// the startup log). Every other field is the environment's, untouched.
func (r *Roots) Config(ctx context.Context) config.Config {
	c := r.env()
	c.MoviesDir = r.Movies(ctx)
	c.TVDir = r.TV(ctx)
	c.EbooksDir = r.Ebooks(ctx)
	c.AudiobooksDir = r.Audiobooks(ctx)
	c.MusicDir = r.Music(ctx)
	c.DownloadsDir = r.Downloads(ctx)
	return c
}

// Func adapts one resolver method to the func() string the importer, the coordinator
// and the disk guard take, reading on a background context: each call is one in-memory
// settings read, and the callers have no request context of their own to hand it.
func Func(f func(ctx context.Context) string) func() string {
	return func() string { return f(context.Background()) }
}
