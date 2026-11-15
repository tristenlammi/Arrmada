package convert

import (
	"context"
	"strings"
	"sync"

	langtab "github.com/tristenlammi/arrmada/internal/langs"
)

// "Same as Subtitles" follows each title's own Subtitles profile, not just its languages
// (#100). A profile of English "Full + forced, Never SDH, only when the audio is another
// language" kept every English track of an English show, SDH included, because only the
// language list reached Convert. Now, for a language the title's profile names:
//
//   - a full track goes when the profile doesn't want the full subtitle for this file
//     (forced only, or the main audio is already that language and the profile says to
//     skip it then);
//   - an SDH track goes under "Never SDH" when a plain full track of the language stays.
//
// A forced track always stays: it's a few lines, and players pick it on their own. A track
// in no language, or a language the profile doesn't name (another profile's, or one added
// beside "Same as Subtitles" by hand), is left to the language list as before. A main
// audio that can't be read keeps every full track: better a subtitle too many than none.

// SubRule is one language of a title's Subtitles profile.
type SubRule struct {
	Lang               string
	Full               bool // the full subtitle is wanted (not forced alone)
	OnlyIfAudioDiffers bool // ...but not when the main audio is already Lang
	NoSDH              bool // "Never SDH"
}

// SubtitleProfiles is the Subtitles module's per-title profiles, wired by main (convert
// must not import subtitles).
type SubtitleProfiles struct {
	// Rules is the profile a title uses. kind is "movie" or "episode".
	Rules func(ctx context.Context, kind string, movieID, seriesID int64) []SubRule
	// Key changes whenever any title's rules could have.
	Key func(ctx context.Context) string
}

// SetSubtitleProfiles wires the per-title profiles.
func (s *Service) SetSubtitleProfiles(sp SubtitleProfiles) { s.subProfiles = sp }

// titleRef is the title a file belongs to, as a plan needs it.
type titleRef struct {
	Kind              string // "movie" | "episode"; "" = not known: no per-title rules
	MovieID, SeriesID int64
	OrigLang          string
}

// subRulesFor returns a lookup of the title rules for one prefs read, cached per title so a
// pass over a thousand episodes asks once per show. nil when the rules don't apply: the
// keep-list doesn't follow Subtitles, or nothing is wired.
func (s *Service) subRulesFor(ctx context.Context, saved string) func(titleRef) []SubRule {
	sp := s.subProfiles
	if sp.Rules == nil || !followsSubtitles(saved) {
		return nil
	}
	extra := map[string]bool{}
	for _, l := range splitCSV(saved) {
		if !strings.EqualFold(l, SameAsSubtitles) {
			extra[langtab.Base(l)] = true
		}
	}
	var mu sync.Mutex
	cache := map[titleRef][]SubRule{}
	return func(t titleRef) []SubRule {
		if t.Kind == "" {
			return nil
		}
		k := titleRef{Kind: t.Kind, MovieID: t.MovieID, SeriesID: t.SeriesID}
		if t.Kind != "movie" {
			k.MovieID = 0
		}
		mu.Lock()
		defer mu.Unlock()
		if r, ok := cache[k]; ok {
			return r
		}
		var out []SubRule
		for _, r := range sp.Rules(ctx, t.Kind, t.MovieID, t.SeriesID) {
			// A language you also listed by hand keeps all its tracks, as listed.
			if !extra[langtab.Base(r.Lang)] {
				out = append(out, r)
			}
		}
		cache[k] = out
		return out
	}
}

// followsSubtitles reports whether a saved keep-list holds the "Same as Subtitles" choice.
func followsSubtitles(saved string) bool {
	for _, l := range splitCSV(saved) {
		if strings.EqualFold(l, SameAsSubtitles) {
			return true
		}
	}
	return false
}

// subRule is the rule for a track's language, if the profile names it.
func subRule(lang string, rules []SubRule) (SubRule, bool) {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" || l == "und" {
		return SubRule{}, false
	}
	for _, r := range rules {
		if langtab.Match(l, langtab.Base(r.Lang)) {
			return r, true
		}
	}
	return SubRule{}, false
}

// isSDH reports a subtitle track for the hard of hearing: flagged, or titled so.
func isSDH(s SubStream) bool {
	if s.HearingImpaired {
		return true
	}
	t := " " + strings.ToLower(s.Title) + " "
	for _, w := range []string{"sdh", "hearing impaired", "hard of hearing", "(hi)", "[hi]", " hi ", "(cc)", "[cc]", " cc "} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// mainAudioLang is the language the file's main audio will be in: the first audio track it
// keeps, as a base code, or "" when that track is untagged.
func mainAudioLang(mi *MediaInfo, plan Plan) string {
	aud := keptAudio(mi, plan)
	if len(aud) == 0 {
		return ""
	}
	l := strings.ToLower(strings.TrimSpace(aud[0].Lang))
	if l == "" || l == "und" {
		return ""
	}
	return langtab.Base(l)
}

// profileSubDrop says whether the title's profile drops one subtitle track, and why. subs
// is every track still in the running, so "Never SDH" can see whether a plain one stays.
func profileSubDrop(s SubStream, subs []SubStream, audio string, rules []SubRule) (bool, string) {
	if s.Forced || len(rules) == 0 {
		return false, ""
	}
	r, ok := subRule(s.Lang, rules)
	if !ok {
		return false, ""
	}
	if !r.Full {
		return true, "the Subtitles profile wants only the forced subtitle"
	}
	if r.OnlyIfAudioDiffers && audio != "" && langtab.Match(audio, langtab.Base(r.Lang)) {
		return true, "the audio is already this language"
	}
	if r.NoSDH && isSDH(s) {
		for _, o := range subs {
			if o.SubIndex != s.SubIndex && !o.Forced && !isSDH(o) && langtab.Match(strings.ToLower(o.Lang), langtab.Base(r.Lang)) {
				return true, "the Subtitles profile says never SDH"
			}
		}
	}
	return false, ""
}

// profileSubs applies the title's profile to the tracks the language list kept.
func profileSubs(subs []SubStream, mi *MediaInfo, plan Plan) []SubStream {
	if len(plan.Subs.Rules) == 0 {
		return subs
	}
	audio := mainAudioLang(mi, plan)
	out := make([]SubStream, 0, len(subs))
	for _, s := range subs {
		if drop, _ := profileSubDrop(s, subs, audio, plan.Subs.Rules); !drop {
			out = append(out, s)
		}
	}
	return out
}
