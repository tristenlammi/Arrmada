package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/music/stage"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// Music placement's HTTP face (MUS-33, MUS-34, MUS-36): the music naming templates with
// their live preview and presets, the house-tag and extras settings, an album's rename
// preview and apply, the library-wide rename, and an album's retag. The work itself is in
// automation (music_placement.go, music_rename.go, music_retag.go).

// musicNamingFields are the music templates in GET/PUT /settings: key, default and the
// check a template must pass before it's saved.
var musicNamingFields = []struct {
	key, def string
	check    func(string) error
}{
	{settings.KeyNamingMusicFolder, library.DefaultMusicFolder, library.ValidateMusicFolder},
	{settings.KeyNamingMusicTrack, library.DefaultMusicTrack, library.ValidateMusicTrack},
	{settings.KeyNamingMusicCompilation, library.DefaultMusicCompilationTrack, library.ValidateMusicTrack},
}

// musicSettings is the saved music placement settings (defaults filled), for GET /settings.
func (a *api) musicSettings(ctx context.Context) map[string]any {
	out := map[string]any{
		settings.KeyMusicWriteTags:              a.deps.Settings.GetBool(ctx, settings.KeyMusicWriteTags, settings.MusicWriteTagsDefault),
		settings.KeyMusicExtras:                 a.deps.Settings.Get(ctx, settings.KeyMusicExtras, stage.DefaultExtras),
		settings.KeyMusicTranscode:              a.deps.Settings.GetBool(ctx, settings.KeyMusicTranscode, settings.MusicTranscodeDefault),
		settings.KeyMusicTranscodeFormat:        a.deps.Settings.Get(ctx, settings.KeyMusicTranscodeFormat, settings.MusicTranscodeFormatDefault),
		settings.KeyMusicTranscodeQuality:       a.deps.Settings.Get(ctx, settings.KeyMusicTranscodeQuality, settings.MusicTranscodeQualityDefault),
		settings.KeyMusicTranscodeLosslessOnly:  a.deps.Settings.GetBool(ctx, settings.KeyMusicTranscodeLosslessOnly, settings.MusicTranscodeLosslessOnlyDefault),
	}
	for _, f := range musicNamingFields {
		out[f.key] = a.deps.Settings.Get(ctx, f.key, f.def)
	}
	return out
}

// musicNamingUpdate pairs each sent template with its key.
func musicNamingUpdate(req *settingsUpdate) map[string]*string {
	return map[string]*string{
		settings.KeyNamingMusicFolder:      req.NamingMusicFolder,
		settings.KeyNamingMusicTrack:       req.NamingMusicTrack,
		settings.KeyNamingMusicCompilation: req.NamingMusicCompilation,
	}
}

// checkMusicSettings validates every sent music template and the extras list, returning
// the first problem in words the settings page shows.
func checkMusicSettings(req *settingsUpdate) string {
	sent := musicNamingUpdate(req)
	for _, f := range musicNamingFields {
		if p := sent[f.key]; p != nil {
			if err := f.check(*p); err != nil {
				return err.Error()
			}
		}
	}
	if req.MusicExtras != nil && len(*req.MusicExtras) > 1000 {
		return "the extras list can be at most 1000 characters"
	}
	if req.MusicTranscodeFormat != nil {
		switch strings.ToLower(strings.TrimSpace(*req.MusicTranscodeFormat)) {
		case "mp3":
		default:
			return "music transcode format must be mp3"
		}
	}
	if req.MusicTranscodeQuality != nil {
		switch strings.ToLower(strings.TrimSpace(*req.MusicTranscodeQuality)) {
		case "v0", "v2", "320", "256", "192":
		default:
			return "music transcode quality must be v0, v2, 320, 256, or 192"
		}
	}
	return ""
}

// saveMusicSettings stores the sent music placement settings, stopping at the first write
// that fails.
func (a *api) saveMusicSettings(ctx context.Context, req *settingsUpdate) error {
	for _, f := range musicNamingFields {
		if p := musicNamingUpdate(req)[f.key]; p != nil {
			if err := a.deps.Settings.Set(ctx, f.key, *p); err != nil {
				return err
			}
		}
	}
	if req.MusicWriteTags != nil {
		if err := a.deps.Settings.SetBool(ctx, settings.KeyMusicWriteTags, *req.MusicWriteTags); err != nil {
			return err
		}
	}
	if req.MusicExtras != nil {
		if err := a.deps.Settings.Set(ctx, settings.KeyMusicExtras, tidyExtras(*req.MusicExtras)); err != nil {
			return err
		}
	}
	if req.MusicTranscode != nil {
		if err := a.deps.Settings.SetBool(ctx, settings.KeyMusicTranscode, *req.MusicTranscode); err != nil {
			return err
		}
	}
	if req.MusicTranscodeFormat != nil {
		if err := a.deps.Settings.Set(ctx, settings.KeyMusicTranscodeFormat, strings.ToLower(strings.TrimSpace(*req.MusicTranscodeFormat))); err != nil {
			return err
		}
	}
	if req.MusicTranscodeQuality != nil {
		if err := a.deps.Settings.Set(ctx, settings.KeyMusicTranscodeQuality, strings.ToLower(strings.TrimSpace(*req.MusicTranscodeQuality))); err != nil {
			return err
		}
	}
	if req.MusicTranscodeLosslessOnly != nil {
		if err := a.deps.Settings.SetBool(ctx, settings.KeyMusicTranscodeLosslessOnly, *req.MusicTranscodeLosslessOnly); err != nil {
			return err
		}
	}
	return nil
}

// tidyExtras stores the extras list as the import reads it: lower case, each with its
// dot, space-separated, in the order given, each once.
func tidyExtras(s string) string {
	allowed := stage.ParseExtras(s)
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\t' }) {
		if !strings.HasPrefix(f, ".") {
			f = "." + f
		}
		if allowed[f] {
			out = append(out, f)
			delete(allowed, f)
		}
	}
	return strings.Join(out, " ")
}

// handleMusicNamingPresets lists the ready-made music schemes and the tokens.
func (a *api) handleMusicNamingPresets(w http.ResponseWriter, _ *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"presets": library.MusicNamingPresets, "tokens": library.MusicTokens})
}

// handleMusicNamingPreview renders the sample albums with the templates in the query
// (folder, track, compilation) as typed, before anything is saved. A template with a
// problem answers 200 with the problem, for the page to mark the field.
func (a *api) handleMusicNamingPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nm := library.MusicNaming{Folder: q.Get("folder"), Track: q.Get("track"), Compilation: q.Get("compilation")}
	samples, perr := library.PreviewMusicNaming(nm)
	if perr != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"samples": []library.MusicNamingSample{}, "error": perr})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"samples": samples})
}

// handleMusicAlbumRenamePreview lists the moves that would put an album's files at their
// names under the current templates. Nothing moves.
func (a *api) handleMusicAlbumRenamePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	items, err := a.deps.Automation.MusicRenamePlan(r.Context(), id)
	if err != nil {
		a.musicErr(w, err, "could not preview the rename")
		return
	}
	if items == nil {
		items = []automation.MusicRenameItem{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleMusicAlbumRename applies the moves the person confirmed (the preview's items, as
// sent back): a move that isn't on the list, or that changed since, stays put.
func (a *api) handleMusicAlbumRename(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Items []automation.MusicRenameItem `json:"items"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Items == nil {
		a.writeError(w, http.StatusBadRequest, "items is required: preview first")
		return
	}
	res, err := a.deps.Automation.MusicRename(r.Context(), id, req.Items)
	if err != nil {
		a.musicErr(w, err, "could not rename the album's files")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// musicOrganizeCounts is what a library-wide preview found.
type musicOrganizeCounts struct {
	Rename   int `json:"rename"`
	Conflict int `json:"conflict"`
	Albums   int `json:"albums"`
}

// handleMusicOrganizePreview is the dry run over every album with files: counts, then the
// moves a page at a time, and a token naming exactly this plan. Nothing moves.
func (a *api) handleMusicOrganizePreview(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = organizePageDefault
	}
	if limit > organizePageMax {
		limit = organizePageMax
	}
	plan, err := a.deps.Automation.MusicRenamePreview(r.Context(), nil)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not preview the rename")
		return
	}
	var counts musicOrganizeCounts
	albums := map[int64]bool{}
	for _, it := range plan {
		if it.Conflict != "" {
			counts.Conflict++
		} else {
			counts.Rename++
		}
		albums[it.AlbumID] = true
	}
	counts.Albums = len(albums)
	total := len(plan)
	page := plan[min(offset, total):min(offset+limit, total)]
	if page == nil {
		page = []automation.MusicRenameItem{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"counts": counts, "items": page, "total": total, "offset": offset, "token": automation.MusicRenameToken(plan),
	})
}

// musicOrganizeSummary is the library-wide rename job's result.
type musicOrganizeSummary struct {
	Albums  int `json:"albums"`
	Renamed int `json:"renamed"`
	Skipped int `json:"skipped"`
}

// handleMusicOrganizeApply starts the renames a library-wide preview showed, as a
// background job. The plan is worked out again first: if it no longer matches the
// preview's token, it answers 409 and nothing moves.
func (a *api) handleMusicOrganizeApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Token == "" {
		a.writeError(w, http.StatusBadRequest, "token is required: preview first")
		return
	}
	plan, err := a.deps.Automation.MusicRenamePreview(r.Context(), nil)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not check the rename")
		return
	}
	if automation.MusicRenameToken(plan) != req.Token {
		a.writeError(w, http.StatusConflict, "the library or the naming changed since the preview — preview again")
		return
	}
	byAlbum := map[int64][]automation.MusicRenameItem{}
	var order []int64
	for _, it := range plan {
		if it.Conflict != "" {
			continue
		}
		if byAlbum[it.AlbumID] == nil {
			order = append(order, it.AlbumID)
		}
		byAlbum[it.AlbumID] = append(byAlbum[it.AlbumID], it)
	}
	spec := jobs.Spec{Kind: "music.organize", Target: "music", Class: jobs.ClassLibraryScan, Timeout: 6 * time.Hour,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			sum := musicOrganizeSummary{Albums: len(order)}
			for i, id := range order {
				// Each album applies exactly its previewed moves; anything that changed
				// since is skipped, never forced.
				res, err := a.deps.Automation.MusicRename(ctx, id, byAlbum[id])
				if err != nil {
					sum.Skipped += len(byAlbum[id])
				} else {
					sum.Renamed += res.Moved
					sum.Skipped += len(res.Skipped)
				}
				p.Set(float64(i+1)/float64(max(len(order), 1)), fmt.Sprintf("Renamed %d file(s) · %d of %d albums", sum.Renamed, i+1, len(order)))
			}
			return sum, nil
		}}
	id, existing, ok := a.submitOr503(w, r, spec)
	if !ok {
		return
	}
	if existing {
		a.alreadyRunning(w, id, "a music rename is already running")
		return
	}
	a.accepted(w, id, false, map[string]any{"status": "organizing", "albums": len(order)})
}

// handleMusicAlbumRetag rewrites an album's files in the house style, as a background job
// (copy, tag, check, swap; originals to the recycle bin).
func (a *api) handleMusicAlbumRetag(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.deps.Music.AlbumRecord(r.Context(), id); err != nil {
		a.musicErr(w, err, "could not load the album")
		return
	}
	spec := jobs.Spec{Kind: "music.retag", Target: strconv.FormatInt(id, 10), Class: jobs.ClassImport, Timeout: 2 * time.Hour,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			res, err := a.deps.Automation.RetagAlbum(ctx, id)
			if err != nil {
				if errors.Is(err, music.ErrNotFound) {
					return res, errors.New("the album has no edition to tag from yet")
				}
				return res, err
			}
			p.Set(1, fmt.Sprintf("Retagged %d file(s) · %d skipped · %d failed", res.Retagged, res.Skipped, res.Failed))
			return res, nil
		}}
	jobID, existing, ok := a.submitOr503(w, r, spec)
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "retagging"})
}
