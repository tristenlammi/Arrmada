package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/videojoin"
)

// Where a finished series download's files land when the episode titles in the filenames
// and the metadata's episode list disagree. Every case runs the real import sweep twice
// (the second pass is the re-process that fills a season's gaps) against a store-backed
// show and reports, for each episode of the season, which file it ended up with: "5:4"
// means episode 5 holds the file numbered E04 in its name, "7:-" means episode 7 is still
// empty. "held" means the download is in Needs you; "blocklisted" that the release was
// blocklisted for the show.
//
// was is the outcome on v0.3.3 and on main before the fix (both run this file unchanged);
// want is now. A change may only turn a wrong placement into "held" or into the right
// placement — never a right one into a wrong one.

// The owner's Nobody Wants This season 3: all ten episodes released at once, while the
// metadata still carried its pre-release listing. E01–E03 agree in both listings; the
// titles of E06 and E09 weren't in the stale one. Only the titles the owner's logs show
// are real — the rest are stand-ins that match nothing.
var nwtStale = map[int]string{
	1: "The Opener", 2: "The Second One", 3: "The Third One",
	4: "The Wedding Planners", 5: "Brian's Wedding", 6: "Episode 6",
	7: "May I Have This Dance", 8: "Watch What Happens", 9: "Episode 9", 10: "I Jew",
}

var nwtFixed = map[int]string{
	1: "The Opener", 2: "The Second One", 3: "The Third One",
	4: "Brian's Wedding", 5: "The Love Card", 6: "Heatwave",
	7: "Watch What Happens", 8: "One in a Million", 9: "Last Christmas", 10: "The Plunge",
}

const nwtPack = "Nobody.Wants.This.S03.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX"

var nwtFiles = []string{
	"Nobody.Wants.This.S03E01.The.Opener.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E02.The.Second.One.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E03.The.Third.One.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E04.Brians.Wedding.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E05.The.Love.Card.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E06.Heatwave.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E07.Watch.What.Happens.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E08.One.in.a.Million.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E09.Last.Christmas.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
	"Nobody.Wants.This.S03E10.The.Plunge.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv",
}

// nwtUntitled is the same pack named the way most packs are: numbers, no titles.
func nwtUntitled() []string {
	out := make([]string, 10)
	for i := range out {
		out[i] = fmt.Sprintf("Nobody.Wants.This.S03E%02d.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv", i+1)
	}
	return out
}

type layoutShow struct {
	title  string
	year   int
	season int
	titles map[int]string
}

var nwtShow = func(titles map[int]string) layoutShow {
	return layoutShow{title: "Nobody Wants This", year: 2024, season: 3, titles: titles}
}

// Parks and Recreation season 6 as TMDB lists it: the two-part "London" is one episode, so
// every release (numbered as TVDB does, London split in two) is one ahead from 6x03 on.
// This is what the title placement was built for.
var parksShow = layoutShow{title: "Parks and Recreation", year: 2009, season: 6, titles: map[int]string{
	1: "London", 2: "The Pawnee-Eagleton Tip-Off Classic", 3: "Doppelgängers", 4: "Gin It Up!", 5: "Filibuster",
}}

// A season the metadata numbers from one but the release one later throughout.
var shiftedShow = layoutShow{title: "Shifted Show", year: 2020, season: 1, titles: map[int]string{
	1: "Alpha Begins Here", 2: "Bravo Comes Next", 3: "Charlie Finishes It",
}}

type titleLayoutCase struct {
	name     string
	show     layoutShow
	download string         // the torrent's name; "" for a single-file torrent (its one file)
	files    []string       // the download's videos
	manual   bool           // grabbed by hand for the season: the quality gate is skipped
	fresh    map[int]string // the metadata source's listing when it differs from the library's
	// partsDiffer: an episode's "Part 2" file is encoded differently from its "Part 1", so
	// the two can't be joined by copying.
	partsDiffer bool
	was, want   string
}

var titleLayoutCases = []titleLayoutCase{
	{
		name: "Nobody Wants This S03 pack, stale metadata (the owner's case)",
		show: nwtShow(nwtStale), download: nwtPack, files: nwtFiles,
		was:  "1:1 2:2 3:3 4:- 5:4 6:6 7:- 8:7 9:9 10:10 blocklisted",
		want: "held 1:- 2:- 3:- 4:- 5:- 6:- 7:- 8:- 9:- 10:-",
	},
	{
		// Grabbed by hand the gate is skipped, so the file numbered E05 overwrites the one
		// moved onto E05 (which goes to the recycle bin) — and E04 and E07 never arrive.
		name: "Nobody Wants This S03 pack, stale metadata, grabbed by hand",
		show: nwtShow(nwtStale), download: nwtPack, files: nwtFiles, manual: true,
		was:  "1:1 2:2 3:3 4:- 5:5 6:6 7:- 8:8 9:9 10:10",
		want: "held 1:- 2:- 3:- 4:- 5:- 6:- 7:- 8:- 9:- 10:-",
	},
	{
		name: "Nobody Wants This S03 pack, corrected metadata",
		show: nwtShow(nwtFixed), download: nwtPack, files: nwtFiles,
		was:  "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
		want: "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
	},
	{
		name: "Nobody Wants This S03 pack without titles, stale metadata",
		show: nwtShow(nwtStale), download: nwtPack, files: nwtUntitled(),
		was:  "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
		want: "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
	},
	{
		// The CAKES single: a number and no title imports by its number.
		name: "single episode, number only, stale metadata",
		show: nwtShow(nwtStale), files: []string{"Nobody.Wants.This.S03E07.1080p.WEB.h264-CAKES.mkv"},
		was:  "1:- 2:- 3:- 4:- 5:- 6:- 7:7 8:- 9:- 10:-",
		want: "1:- 2:- 3:- 4:- 5:- 6:- 7:7 8:- 9:- 10:-",
	},
	{
		// One file alone can't show the metadata is stale: its title names episode 5 and
		// nothing in the download disagrees. It is placed by title as before — the same
		// rule that files a lone Parks 6x03 correctly.
		name: "single episode with a title, stale metadata",
		show: nwtShow(nwtStale), files: []string{nwtFiles[3]},
		was:  "1:- 2:- 3:- 4:- 5:4 6:- 7:- 8:- 9:- 10:-",
		want: "1:- 2:- 3:- 4:- 5:4 6:- 7:- 8:- 9:- 10:-",
	},
	{
		// Likewise a partial pack whose moved file lands on an episode no other file in it
		// claims: nothing in the download contradicts the title.
		name: "partial pack, one file moved by title onto an unclaimed episode",
		show: nwtShow(nwtStale), download: "Nobody.Wants.This.S03.Part.1.1080p.NF.WEB-DL.DDP5.1.Atmos.H.264-FLUX", files: nwtFiles[:4],
		was:  "1:1 2:2 3:3 4:- 5:4 6:- 7:- 8:- 9:- 10:- blocklisted",
		want: "1:1 2:2 3:3 4:- 5:4 6:- 7:- 8:- 9:- 10:- blocklisted",
	},
	{
		// The case title placement exists for: the two-part file keeps its two numbers
		// and every later file moves back one, onto an episode nothing else claims except
		// the two-parter's second half.
		name: "Parks and Recreation S06, TMDB merged the two-part London",
		show: parksShow, download: "Parks.and.Recreation.S06.1080p.WEB-DL.x264-GRP", files: []string{
			"Parks and Recreation - 6x01 & 6x02 - London.mkv",
			"Parks and Recreation - 6x03 - The Pawnee-Eagleton Tip-Off Classic.mkv",
			"Parks and Recreation - 6x04 - Doppelgängers.mkv",
			"Parks and Recreation - 6x05 - Gin It Up!.mkv",
			"Parks and Recreation - 6x06 - Filibuster.mkv",
		},
		was:  "1:1+2 2:1+2 3:4 4:5 5:6",
		want: "1:1+2 2:1+2 3:4 4:5 5:6",
	},
	{
		name: "Parks and Recreation S06, grabbed by hand",
		show: parksShow, download: "Parks.and.Recreation.S06.1080p.WEB-DL.x264-GRP", manual: true, files: []string{
			"Parks and Recreation - 6x01 & 6x02 - London.mkv",
			"Parks and Recreation - 6x03 - The Pawnee-Eagleton Tip-Off Classic.mkv",
			"Parks and Recreation - 6x04 - Doppelgängers.mkv",
			"Parks and Recreation - 6x05 - Gin It Up!.mkv",
			"Parks and Recreation - 6x06 - Filibuster.mkv",
		},
		was:  "1:1+2 2:3 3:4 4:5 5:6",
		want: "1:1+2 2:3 3:4 4:5 5:6",
	},
	{
		name: "Nobody Wants This S03 pack, corrected metadata, grabbed by hand",
		show: nwtShow(nwtFixed), download: nwtPack, files: nwtFiles, manual: true,
		was:  "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
		want: "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
	},
	{
		name: "whole pack numbered one ahead of the metadata, every file titled",
		show: shiftedShow, download: "Shifted.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Shifted.Show.S01E02.Alpha.Begins.Here.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E03.Bravo.Comes.Next.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E04.Charlie.Finishes.It.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:2 2:3 3:4",
		want: "1:2 2:3 3:4",
	},
	{
		// Two copies of one episode, both by number: not a title disagreement. The first
		// is placed and the second is weighed against it, as before.
		name: "two copies of one episode by number",
		show: shiftedShow, download: "Shifted.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Shifted.Show.S01E01.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E01.REPACK.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E02.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:- blocklisted",
		want: "1:1 2:2 3:- blocklisted",
	},
	{
		// The metadata source already has the corrected listing; the library's is the
		// pre-release one. The disagreement refreshes the show, and the pack then imports
		// by its numbers, every title now agreeing.
		name: "Nobody Wants This S03 pack, stale library listing, source corrected",
		show: nwtShow(nwtStale), fresh: nwtFixed, download: nwtPack, files: nwtFiles,
		was:  "1:1 2:2 3:3 4:- 5:4 6:6 7:- 8:7 9:9 10:10 blocklisted",
		want: "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
	},
	{
		name: "Nobody Wants This S03 pack, stale library listing, source corrected, grabbed by hand",
		show: nwtShow(nwtStale), fresh: nwtFixed, download: nwtPack, files: nwtFiles, manual: true,
		was:  "1:1 2:2 3:3 4:- 5:5 6:6 7:- 8:8 9:9 10:10",
		want: "1:1 2:2 3:3 4:4 5:5 6:6 7:7 8:8 9:9 10:10",
	},
	{
		// Two copies of one episode that move together by their title: not a disagreement.
		name: "Parks and Recreation S06 with a REPACK of 6x05",
		show: parksShow, download: "Parks.and.Recreation.S06.1080p.WEB-DL.x264-GRP", files: []string{
			"Parks and Recreation - 6x01 & 6x02 - London.mkv",
			"Parks and Recreation - 6x03 - The Pawnee-Eagleton Tip-Off Classic.mkv",
			"Parks and Recreation - 6x04 - Doppelgängers.mkv",
			"Parks and Recreation - 6x05 - Gin It Up!.mkv",
			"Parks and Recreation - 6x06 - Filibuster.mkv",
			"Parks and Recreation - 6x05 - Gin It Up! REPACK.mkv",
		},
		was:  "1:1+2 2:1+2 3:4 4:5 5:6",
		want: "1:1+2 2:1+2 3:4 4:5 5:6",
	},
	{
		name: "whole pack numbered one ahead, with a PROPER beside E03",
		show: shiftedShow, download: "Shifted.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Shifted.Show.S01E02.Alpha.Begins.Here.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E03.Bravo.Comes.Next.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E04.Charlie.Finishes.It.1080p.WEB-DL.x264-GRP.mkv",
			"Shifted.Show.S01E03.Bravo.Comes.Next.PROPER.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:2 2:3 3:4",
		want: "1:2 2:3 3:4",
	},
	{
		// A two-parter the release splits and the metadata merges. First Part 2 was skipped
		// in silence, then the download was held (the two files do claim one episode); now
		// the parts are joined into the one episode they are, and the rest follow by title.
		name: "two-parter split by the release, merged in the metadata",
		show: layoutShow{title: "Long Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here"}},
		download: "Long.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E02.The.Long.Goodbye.Begins.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E03.Second.Story.Here.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E04.Third.Story.Here.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "held 1:- 2:- 3:-",
		want: "1:1&2 2:3 3:4",
	},
	{
		// The owner's case. Disney+ cuts "The Simpsons Guy" in two and counts both, and its
		// title for episode 3 drops the list's "The": nothing names that file's episode, so
		// it is placed one earlier on the strength of the parts before it, which every
		// title that does name an episode agrees with.
		name: "Family Guy S13 (Disney+): parts joined, an untitled-by-the-list file moved with them",
		show: fgS13, download: "Family.Guy.S13.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT", files: []string{
			"Family.Guy.S13E01.The.Simpsons.Guy.Part.1.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E02.The.Simpsons.Guy.Part.2.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E03.Book.of.Joe.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E04.Baking.Bad.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E05.Brian.the.Closer.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
		},
		was:  "held 1:- 2:- 3:- 4:-",
		want: "1:1&2 2:3 3:4 4:5",
	},
	{
		name: "Family Guy S13 (Disney+), grabbed by hand",
		show: fgS13, download: "Family.Guy.S13.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT", manual: true, files: []string{
			"Family.Guy.S13E01.The.Simpsons.Guy.Part.1.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E02.The.Simpsons.Guy.Part.2.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E03.Book.of.Joe.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E04.Baking.Bad.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E05.Brian.the.Closer.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
		},
		was:  "held 1:- 2:- 3:- 4:-",
		want: "1:1&2 2:3 3:4 4:5",
	},
	{
		// The parts were encoded differently: joining them by copying would give a file
		// that breaks at the seam, so the download is held and says so.
		name: "Family Guy S13 (Disney+), the two parts encoded differently",
		show: fgS13, download: "Family.Guy.S13.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT", partsDiffer: true, files: []string{
			"Family.Guy.S13E01.The.Simpsons.Guy.Part.1.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E02.The.Simpsons.Guy.Part.2.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E03.Book.of.Joe.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E04.Baking.Bad.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S13E05.Brian.the.Closer.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
		},
		was:  "held 1:- 2:- 3:- 4:-",
		want: "held 1:- 2:- 3:- 4:-",
	},
	{
		// Season 9: the release shortens titles the list has in full ("Broadcasting" for
		// "Excellence in Broadcasting"), so they name nothing; "Pt" for "Part".
		name: "Family Guy S09 (Disney+): Pt 1 / Pt 2, titles shortened by the release",
		show: fgS09, download: "Family.Guy.S09.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT", files: []string{
			"Family.Guy.S09E01.And.Then.There.Were.Fewer.Pt.1.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S09E02.And.Then.There.Were.Fewer.Pt.2.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S09E03.Broadcasting.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S09E04.Welcome.Back.Carter.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S09E05.Halloween.on.Spooner.Street.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S09E06.Baby.You.Out.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
			"Family.Guy.S09E07.Brian.Writes.a.Bestseller.1080p.DSNP.WEB-DL.AAC2.0.H.264-OldT.mkv",
		},
		was:  "held 1:- 2:- 3:- 4:- 5:- 6:-",
		want: "1:1&2 2:3 3:4 4:5 5:6 6:7",
	},
	{
		// Season 6: beside the split "Blue Harvest" the release has a "100th Episode
		// Special" the season's list doesn't, so "Long John Peter" is two ahead where the
		// parts explain one. The titles don't bear the arithmetic out: nothing is moved by
		// it, and the download is held for a person as before.
		name: "Family Guy S06 (Disney+): an extra episode the list lacks, still held",
		show: fgS06, download: "Family.Guy.S06.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1", files: []string{
			"Family.Guy.S06E01.Blue.Harvest.Part.1.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
			"Family.Guy.S06E02.Blue.Harvest.Part.2.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
			"Family.Guy.S06E03.Movin.Out.Brians.Song.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
			"Family.Guy.S06E04.100th.Episode.Special.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
			"Family.Guy.S06E05.Believe.It.or.Not.Joes.Walking.on.Air.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
			"Family.Guy.S06E06.Stewie.Kills.Lois.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
			"Family.Guy.S06E07.Lois.Kills.Stewie.1080p.DSNP.WEB-DL.AAC2.0.H.264.DUAL-Yatogam1.mkv",
		},
		was:  "held 1:- 2:- 3:- 4:- 5:-",
		want: "held 1:- 2:- 3:- 4:- 5:-",
	},
	{
		// A list that counts the parts as the release does needs no joining: by number.
		name: "two-parter the metadata lists as two episodes too",
		show: layoutShow{title: "Long Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Long Goodbye Begins (1)", 2: "The Long Goodbye Begins, Part 2", 3: "Second Story Here"}},
		download: "Long.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E02.The.Long.Goodbye.Begins.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E03.Second.Story.Here.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:3",
		want: "1:1 2:2 3:3",
	},
	{
		// Both parts under one number, and nothing after them titled: nothing confirms
		// they're one episode, so as before (Part 2 weighed as a copy of Part 1).
		name: "two parts the release gave one number",
		show: layoutShow{title: "Long Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here"}},
		download: "Long.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E02.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E03.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:3",
		want: "1:1 2:2 3:3",
	},
	{
		// Only the parts carry titles: nothing after them confirms the count, so nothing
		// is joined or moved, and it is held as before.
		name: "split two-parter, the rest of the pack numbered only",
		show: layoutShow{title: "Long Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here"}},
		download: "Long.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E02.The.Long.Goodbye.Begins.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E03.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E04.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "held 1:- 2:- 3:-",
		want: "held 1:- 2:- 3:-",
	},
	{
		// Two real episodes the list names "Homecoming" and "Homecoming (2)", released as
		// Part 1 and Part 2: never joined, each by its number as before.
		name: "two real episodes, the list numbering the second half its own way",
		show: layoutShow{title: "Home Show", year: 2020, season: 1, titles: map[int]string{
			1: "Opening", 2: "Homecoming", 3: "Homecoming (2)", 4: "Aftermath"}},
		download: "Home.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Home.Show.S01E01.Opening.1080p.WEB-DL.x264-GRP.mkv",
			"Home.Show.S01E02.Homecoming.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Home.Show.S01E03.Homecoming.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Home.Show.S01E04.Aftermath.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:3 4:4",
		want: "1:1 2:2 3:3 4:4",
	},
	{
		// Two real episodes the list names differently ("Pilot", "Farewell"), released as
		// Pilot Part 1 and Part 2, then numbered files: never joined or shifted.
		name: "two real episodes the list names differently, the rest numbered only",
		show: layoutShow{title: "Home Show", year: 2020, season: 1, titles: map[int]string{
			1: "Pilot", 2: "Farewell", 3: "Third Story Here", 4: "Fourth"}},
		download: "Home.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Home.Show.S01E01.Pilot.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Home.Show.S01E02.Pilot.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Home.Show.S01E03.1080p.WEB-DL.x264-GRP.mkv",
			"Home.Show.S01E04.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:3 4:4",
		want: "1:1 2:2 3:3 4:4",
	},
	{
		// A four-part story: parts 1 to 3 are never joined without Part 4.
		name: "a four-part story",
		show: layoutShow{title: "Beast Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Beast", 2: "Second", 3: "Third", 4: "Fourth", 5: "Fifth"}},
		download: "Beast.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Beast.Show.S01E01.The.Beast.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Beast.Show.S01E02.The.Beast.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Beast.Show.S01E03.The.Beast.Part.3.1080p.WEB-DL.x264-GRP.mkv",
			"Beast.Show.S01E04.The.Beast.Part.4.1080p.WEB-DL.x264-GRP.mkv",
			"Beast.Show.S01E05.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:3 4:4 5:5",
		want: "1:1 2:2 3:3 4:4 5:5",
	},
	{
		// A two-episode file after the parts can't be counted: held as before.
		name: "split two-parter, then a two-episode file",
		show: layoutShow{title: "Long Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here", 4: "Fourth Story Here", 5: "Fifth Story Here"}},
		download: "Long.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E02.The.Long.Goodbye.Begins.Part.2.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E03.Second.Story.Here.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E04E05.1080p.WEB-DL.x264-GRP.mkv",
			"Long.Show.S01E06.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "held 1:- 2:- 3:- 4:- 5:-",
		want: "held 1:- 2:- 3:- 4:- 5:-",
	},
	{
		// One part alone is not an episode in parts: as before.
		name: "only Part 1 of a split two-parter",
		show: layoutShow{title: "Long Show", year: 2020, season: 1, titles: map[int]string{
			1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here"}},
		files: []string{"Long.Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv"},
		was:   "1:1 2:- 3:-",
		want:  "1:1 2:- 3:-",
	},
	{
		// An episode number the season doesn't have, titled like an episode another file
		// claims by its number. Before, the stray E09 was skipped in silence; now the
		// disagreement is held for a person.
		name: "out-of-range number moved by title onto a claimed episode",
		show: layoutShow{title: "Oor Show", year: 2020, season: 1, titles: map[int]string{
			1: "Alpha Episode Name", 2: "Bravo Episode Name", 3: "Charlie Episode Name"}},
		download: "Oor.Show.S01.1080p.WEB-DL.x264-GRP", files: []string{
			"Oor.Show.S01E01.Alpha.Episode.Name.1080p.WEB-DL.x264-GRP.mkv",
			"Oor.Show.S01E02.Bravo.Episode.Name.1080p.WEB-DL.x264-GRP.mkv",
			"Oor.Show.S01E03.Charlie.Episode.Name.1080p.WEB-DL.x264-GRP.mkv",
			"Oor.Show.S01E09.Charlie.Episode.Name.1080p.WEB-DL.x264-GRP.mkv",
		},
		was:  "1:1 2:2 3:3",
		want: "held 1:- 2:- 3:-",
	},
}

// Family Guy as TMDB lists it, where Disney+ cuts each double-length episode in two.
var (
	fgS13 = layoutShow{title: "Family Guy", year: 1999, season: 13, titles: map[int]string{
		1: "The Simpsons Guy", 2: "The Book of Joe", 3: "Baking Bad", 4: "Brian the Closer"}}
	fgS09 = layoutShow{title: "Family Guy", year: 1999, season: 9, titles: map[int]string{
		1: "And Then There Were Fewer", 2: "Excellence in Broadcasting", 3: "Welcome Back, Carter",
		4: "Halloween on Spooner Street", 5: "Baby, You Knock Me Out", 6: "Brian Writes a Bestseller"}}
	fgS06 = layoutShow{title: "Family Guy", year: 1999, season: 6, titles: map[int]string{
		1: "Blue Harvest", 2: "Movin' Out (Brian's Song)", 3: "Believe It or Not, Joe's Walking on Air",
		4: "Stewie Kills Lois", 5: "Lois Kills Stewie"}}
)

func TestTitlePlacementLayoutMatrix(t *testing.T) {
	var changed []string
	for _, tc := range titleLayoutCases {
		t.Run(tc.name, func(t *testing.T) {
			got := runTitleLayoutCase(t, tc)
			if got != tc.want {
				t.Errorf("got  %s\nwant %s\n(was %s)", got, tc.want, tc.was)
			}
			if tc.was != tc.want {
				changed = append(changed, fmt.Sprintf("%s: %s → %s", tc.name, tc.was, tc.want))
			}
		})
	}
	for _, c := range changed {
		t.Log("changed: " + c)
	}
}

// runTitleLayoutCase imports one case's download through the sweep and describes where
// its files landed.
func runTitleLayoutCase(t *testing.T, tc titleLayoutCase) string {
	t.Helper()
	h := newStallHarness(t)
	h.c.imp = library.NewImporter(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := seedTitledSeason(t, h, tc.show)
	if tc.fresh != nil {
		// The metadata source has the corrected listing; the library still has the old.
		fresh := tc.show
		fresh.titles = tc.fresh
		serveListing(t, h, fresh)
	}
	// An episode's parts are "joined" into a file as big as them together, which is how
	// describeSeason tells it from every file of the download.
	h.c.probeVideoFn = func(_ context.Context, path string) (videojoin.Layout, error) {
		codec := "h264"
		if tc.partsDiffer && strings.Contains(path, "Part.2") {
			codec = "hevc"
		}
		return videojoin.Layout{Streams: []videojoin.Stream{{Type: "video", Codec: codec}}, Duration: 1320}, nil
	}
	h.c.joinVideoFn = func(_ context.Context, parts []string, _ []videojoin.Layout, out string) error {
		var size int64
		for _, p := range parts {
			fi, err := os.Stat(p)
			if err != nil {
				return err
			}
			size += fi.Size()
		}
		writeSparse(t, out, size)
		return nil
	}
	hash, name, _ := dropDownload(t, h, tc.download, tc.files)
	if tc.manual {
		h.c.recordSeriesGrab(h.ctx, id, name, "Fake", "", hash)
		h.c.markGrab(h.ctx, hash, true, GrabScope{Season: tc.show.season})
	}
	h.c.ImportSeriesDownloads(h.ctx)
	h.c.ImportSeriesDownloads(h.ctx)
	return describeSeason(t, h, id, tc.show.season, hash, tc.files)
}

// seedTitledSeason adds a show with one aired, monitored season carrying titles.
func seedTitledSeason(t *testing.T, h *stallHarness, sh layoutShow) int64 {
	t.Helper()
	repo := series.NewRepo(h.c.db)
	sr, err := repo.Create(h.ctx, series.Series{TMDBID: 77, Title: sh.title, Year: sh.year, Monitored: true, Status: "Ended"})
	if err != nil {
		t.Fatal(err)
	}
	season := series.Season{SeasonNumber: sh.season, Monitored: true}
	for n := 1; n <= len(sh.titles); n++ {
		season.Episodes = append(season.Episodes, series.Episode{
			SeasonNumber: sh.season, EpisodeNumber: n, Title: sh.titles[n],
			AirDate: "2025-10-23", Runtime: 30, Monitored: true,
		})
	}
	if err := repo.InsertSeasons(h.ctx, sr.ID, []series.Season{season}); err != nil {
		t.Fatal(err)
	}
	serveListing(t, h, sh) // the metadata source agrees with the library until a test says otherwise
	return sr.ID
}

// listingMeta is a metadata source answering one show's listing, counting the asks.
type listingMeta struct {
	mu    sync.Mutex
	d     metadata.SeriesDetails
	calls int
}

func (m *listingMeta) Available() bool { return true }
func (m *listingMeta) SearchSeries(context.Context, string) ([]metadata.SeriesResult, error) {
	return nil, nil
}
func (m *listingMeta) GetSeries(context.Context, int) (*metadata.SeriesDetails, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	cp := m.d
	return &cp, nil
}

func (m *listingMeta) asked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// serveListing points the coordinator's series module at a metadata source whose listing
// for the show is sh.
func serveListing(t *testing.T, h *stallHarness, sh layoutShow) *listingMeta {
	sd := metadata.SeasonDetails{SeasonNumber: sh.season}
	for n := 1; n <= len(sh.titles); n++ {
		sd.Episodes = append(sd.Episodes, metadata.EpisodeDetails{EpisodeNumber: n, Title: sh.titles[n], AirDate: "2025-10-23", Runtime: 30})
	}
	m := &listingMeta{d: metadata.SeriesDetails{
		SeriesResult:    metadata.SeriesResult{TMDBID: 77, Title: sh.title, Year: sh.year},
		Status:          "Ended",
		NumberingSource: "tmdb",
		Seasons:         []metadata.SeasonDetails{sd},
	}}
	h.c.series = series.NewService(h.c.db, m, t.TempDir(), h.c.log)
	return m
}

// dropDownload writes a finished torrent's videos (each a distinct size, so the library
// can say which one it holds) and lists it in the TV category of the fake client.
// download "" is a single-file torrent of files[0].
func dropDownload(t *testing.T, h *stallHarness, download string, files []string) (hash, name, content string) {
	t.Helper()
	dir := t.TempDir()
	if download != "" {
		dir = filepath.Join(dir, download)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for i, f := range files {
		writeSparse(t, filepath.Join(dir, f), layoutFileSize(i))
	}
	name, content = download, dir
	if download == "" {
		name = strings.TrimSuffix(files[0], filepath.Ext(files[0]))
		content = filepath.Join(dir, files[0])
	}
	hash = hashFor(name)
	h.qbit.mu.Lock()
	h.qbit.torrents = append(h.qbit.torrents, map[string]any{
		"hash": hash, "name": name, "state": "uploading", "progress": 1.0,
		"size": int64(len(files)) * layoutFileSize(0), "amount_left": 0, "category": seriesCategory, "content_path": content,
	})
	h.qbit.mu.Unlock()
	return hash, name, content
}

func layoutFileSize(i int) int64 { return int64(60+i) << 20 }

func writeSparse(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// describeSeason renders "held"/"blocklisted" and, per episode, the number(s) in the name
// of the file it holds — the file found by its size.
func describeSeason(t *testing.T, h *stallHarness, id int64, season int, hash string, files []string) string {
	t.Helper()
	var parts []string
	if reviews, _ := h.c.ListReviews(h.ctx); len(reviews) > 0 {
		for _, r := range reviews {
			if r.Hash == hash {
				parts = append(parts, "held")
				break
			}
		}
	}
	s, err := h.c.series.Get(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, sn := range s.Seasons {
		if sn.SeasonNumber != season {
			continue
		}
		for _, ep := range sn.Episodes {
			holds := "-"
			if ep.HasFile {
				holds = "?"
				for i, f := range files {
					if ep.SizeBytes == layoutFileSize(i) {
						holds = fileNumbers(f)
					}
					// Two files joined into one: "1&2".
					if i+1 < len(files) && ep.SizeBytes == layoutFileSize(i)+layoutFileSize(i+1) {
						holds = fileNumbers(f) + "&" + fileNumbers(files[i+1])
					}
				}
			}
			parts = append(parts, fmt.Sprintf("%d:%s", ep.EpisodeNumber, holds))
		}
	}
	if h.blocklistCount(t) > 0 {
		parts = append(parts, "blocklisted")
	}
	return strings.Join(parts, " ")
}

// fileNumbers is the episode number(s) a filename states: "4", or "1+2" for a two-parter.
func fileNumbers(name string) string {
	eps := parser.Parse(name).Episodes
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = strconv.Itoa(e)
	}
	return strings.Join(out, "+")
}
