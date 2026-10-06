package audioserver

import (
	"context"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/listening"
)

// Replies shaped like Audiobookshelf's, field for field where clients read them. Lissen's
// models were checked against its source; the extra fields match what Audiobookshelf
// itself sends, so other clients find what they expect too.

// ServerVersion is the Audiobookshelf version this server reports. Clients gate newer
// features (refresh tokens, paged authors) on it.
const ServerVersion = "2.37.0"

type obj = map[string]any

func metadataMinified(it Item) obj {
	authorName := it.Book.Author
	seriesName := ""
	if it.Book.SeriesName != "" {
		seriesName = it.Book.SeriesName
		if seq := seriesSequence(it.Book); seq != "" {
			seriesName += " #" + seq
		}
	}
	return obj{
		"title": it.Title, "titleIgnorePrefix": ignorePrefix(it.Title), "subtitle": nil,
		"authorName": authorName, "authorNameLF": authorNameLF(authorName), "narratorName": "",
		"seriesName": seriesName, "genres": nonNilStrings(it.Book.Subjects), "publishedYear": yearString(it.Book.Year),
		"publishedDate": nil, "publisher": nil, "description": it.Book.Description, "isbn": nil, "asin": nil,
		"language": nil, "explicit": false, "abridged": false,
	}
}

func metadataExpanded(it Item) obj {
	m := metadataMinified(it)
	authors := []obj{}
	for _, a := range splitAuthors(it.Book.Author) {
		authors = append(authors, obj{"id": authorID(a), "name": a})
	}
	series := []obj{}
	if it.Book.SeriesName != "" {
		var seq any
		if s := seriesSequence(it.Book); s != "" {
			seq = s
		}
		series = append(series, obj{"id": seriesID(it.Book.SeriesName), "name": it.Book.SeriesName, "sequence": seq})
	}
	m["authors"], m["narrators"], m["series"] = authors, []string{}, series
	m["descriptionPlain"] = it.Book.Description
	return m
}

// itemMinified is an item in lists; files are only described if already probed.
func (s *Server) itemMinified(ctx context.Context, it Item, progress map[string]listening.Progress) obj {
	files, _ := s.probe.files(ctx, it.Path, false)
	dur := totalDuration(files)
	chapters := 0
	if dur > 0 {
		chapters = len(bookChapters(files))
	}
	o := obj{
		"id": it.Key, "ino": inoFor(it.Key), "oldLibraryItemId": nil, "libraryId": libraryID, "folderId": libraryID,
		"path": it.Path, "relPath": it.Title, "isFile": len(files) == 1, "mtimeMs": it.AddedAt, "ctimeMs": it.AddedAt,
		"birthtimeMs": it.AddedAt, "addedAt": it.AddedAt, "updatedAt": it.AddedAt, "isMissing": false, "isInvalid": false,
		"mediaType": "book",
		"media": obj{
			"id": "m" + it.Key, "metadata": metadataMinified(it), "coverPath": coverPath(it), "tags": []string{},
			"numTracks": len(files), "numAudioFiles": len(files), "numChapters": chapters, "duration": dur,
			"size": totalSize(files), "ebookFormat": nil,
		},
		"numFiles": len(files), "size": totalSize(files),
	}
	if p, ok := progress[it.Key]; ok {
		o["userMediaProgress"] = mediaProgress(p)
	}
	return o
}

// itemExpanded is the full item: files, chapters and play tracks. It probes files that
// aren't known yet.
func (s *Server) itemExpanded(ctx context.Context, it Item, p *listening.Progress) (obj, []AudioFile) {
	files, _ := s.probe.files(ctx, it.Path, true)
	audio := []obj{}
	tracks := []obj{}
	offset := 0.0
	for _, f := range files {
		meta := obj{"filename": f.Name, "ext": f.Ext, "path": f.Path, "relPath": f.Name, "size": f.Size,
			"mtimeMs": f.MTimeMs, "ctimeMs": f.MTimeMs, "birthtimeMs": f.MTimeMs}
		af := obj{
			"index": f.Index, "ino": f.Ino, "metadata": meta, "addedAt": it.AddedAt, "updatedAt": f.MTimeMs,
			"trackNumFromMeta": nil, "discNumFromMeta": nil, "trackNumFromFilename": f.Index, "discNumFromFilename": nil,
			"manuallyVerified": false, "exclude": false, "error": nil, "format": strings.TrimPrefix(f.Ext, "."),
			"duration": f.Duration, "bitRate": f.Bitrate, "language": nil, "codec": f.Codec, "timeBase": "1/44100",
			"channels": 2, "channelLayout": "stereo", "chapters": nonNilChapters(f.Chapters), "embeddedCoverArt": nil,
			"metaTags": obj{"tagTitle": nilIfEmpty(f.Title)}, "mimeType": mimeFor(f.Ext),
		}
		audio = append(audio, af)
		// Audiobookshelf's play track is the whole audio file plus where it starts and how
		// to fetch it; clients that read file fields off a track find them.
		track := obj{}
		for k, v := range af {
			track[k] = v
		}
		track["startOffset"], track["title"] = offset, firstNonEmpty(f.Title, f.Name)
		track["contentUrl"] = "/api/items/" + it.Key + "/file/" + f.Ino
		tracks = append(tracks, track)
		offset += f.Duration
	}
	chapters := bookChapters(files)
	if chapters == nil {
		chapters = []Chapter{}
	}
	o := obj{
		"id": it.Key, "ino": inoFor(it.Key), "oldLibraryItemId": nil, "libraryId": libraryID, "folderId": libraryID,
		"path": it.Path, "relPath": it.Title, "isFile": len(files) == 1, "mtimeMs": it.AddedAt, "ctimeMs": it.AddedAt,
		"birthtimeMs": it.AddedAt, "addedAt": it.AddedAt, "updatedAt": it.AddedAt, "lastScan": nil, "scanVersion": nil,
		"isMissing": false, "isInvalid": false, "mediaType": "book",
		"media": obj{
			"id": "m" + it.Key, "libraryItemId": it.Key, "metadata": metadataExpanded(it), "coverPath": coverPath(it),
			"tags": []string{}, "audioFiles": audio, "chapters": chapters, "duration": totalDuration(files),
			"size": totalSize(files), "tracks": tracks, "ebookFile": nil, "ebookFormat": nil, "numTracks": len(files),
			"numAudioFiles": len(files), "numChapters": len(chapters),
		},
		// Since Audiobookshelf 2.36 the expanded item carries every list field too.
		"libraryFiles": []obj{}, "numFiles": len(files), "size": totalSize(files),
	}
	if p != nil {
		o["userMediaProgress"] = mediaProgress(*p)
	}
	return o, files
}

func mediaProgress(p listening.Progress) obj {
	var finishedAt any
	if p.FinishedAt > 0 {
		finishedAt = p.FinishedAt
	}
	return obj{
		"id": p.ItemKey, "libraryItemId": p.ItemKey, "episodeId": nil, "mediaItemId": "m" + p.ItemKey,
		"mediaItemType": "book", "duration": p.Duration, "progress": p.Fraction(), "currentTime": p.Position,
		"isFinished": p.Finished, "hideFromContinueListening": p.Hidden, "ebookLocation": nil, "ebookProgress": nil,
		"lastUpdate": p.UpdatedAt, "startedAt": p.UpdatedAt, "finishedAt": finishedAt,
	}
}

func bookmarkJSON(b listening.Bookmark) obj {
	return obj{"libraryItemId": b.ItemKey, "time": b.Time, "title": b.Title, "createdAt": b.CreatedAt}
}

// userJSON is the signed-in user as Audiobookshelf describes one.
func (s *Server) userJSON(ctx context.Context, u *auth.User, t *Tokens) obj {
	progress, _ := s.listen.AllProgress(ctx, u.ID)
	mp := []obj{}
	for _, p := range progress {
		mp = append(mp, mediaProgress(p))
	}
	bms, _ := s.listen.Bookmarks(ctx, u.ID, "")
	bm := []obj{}
	for _, b := range bms {
		bm = append(bm, bookmarkJSON(b))
	}
	typ := "user"
	switch u.Role {
	case auth.RoleAdmin:
		typ = "admin"
	case auth.RoleReadonly:
		typ = "guest"
	}
	o := obj{
		"id": "u" + itoa(u.ID), "username": u.Username, "email": nil, "type": typ, "isActive": !u.Disabled,
		"isLocked": false, "lastSeen": time.Now().UnixMilli(), "createdAt": parseAdded(u.CreatedAt),
		"mediaProgress": mp, "seriesHideFromContinueListening": []string{}, "bookmarks": bm,
		"permissions": obj{"download": true, "update": false, "delete": false, "upload": false,
			"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true},
		"librariesAccessible": []string{}, "itemTagsSelected": []string{}, "hasOpenIDLink": false,
	}
	if t != nil {
		o["token"] = nilIfEmpty(t.Legacy)
		o["accessToken"] = t.Access
		o["refreshToken"] = t.Refresh
	}
	return o
}

func (s *Server) loginJSON(ctx context.Context, u *auth.User, t *Tokens) obj {
	return obj{
		"user": s.userJSON(ctx, u, t), "userDefaultLibraryId": libraryID,
		"serverSettings": s.serverSettings(), "ereaderDevices": []obj{}, "Source": "docker",
	}
}

func (s *Server) serverSettings() obj {
	return obj{
		"id": "server-settings", "version": ServerVersion, "buildNumber": 1, "language": "en-us",
		"authActiveAuthMethods": []string{"local"}, "authOpenIDAutoLaunch": false, "authOpenIDButtonText": "",
		"sortingIgnorePrefix": true, "sortingPrefixes": []string{"the", "a", "an"}, "chromecastEnabled": false,
		"dateFormat": "MM/dd/yyyy", "timeFormat": "HH:mm", "homeBookshelfView": 1, "bookshelfView": 1,
		"podcastEpisodeSchedule": "0 * * * *", "logLevel": 2,
	}
}

func libraryJSON() obj {
	return obj{
		"id": libraryID, "name": "Audiobooks", "folders": []obj{{"id": libraryID, "fullPath": "/", "libraryId": libraryID, "addedAt": 0}},
		"displayOrder": 1, "icon": "audiobookshelf", "mediaType": "book", "provider": "audible",
		// Audiobookshelf's full default set for a book library: an app that reads the
		// settings into a fixed model finds every one.
		"settings": obj{"coverAspectRatio": 1, "disableWatcher": true, "skipMatchingMediaWithAsin": false,
			"skipMatchingMediaWithIsbn": false, "autoScanCronExpression": nil, "audiobooksOnly": true, "hideSingleBookSeries": false,
			"epubsAllowScriptedContent": false, "onlyShowLaterBooksInContinueSeries": false,
			"markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10,
			"metadataPrecedence": []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}},
		"lastScan": nil, "lastScanVersion": nil, "createdAt": 0, "lastUpdate": 0,
	}
}

func coverPath(it Item) any {
	if it.Book.CoverURL == "" {
		return nil
	}
	return "/api/items/" + it.Key + "/cover"
}

func ignorePrefix(t string) string {
	l := strings.TrimSpace(t)
	for _, a := range []string{"The ", "A ", "An "} {
		if strings.HasPrefix(l, a) {
			return l[len(a):] + ", " + strings.TrimSpace(a)
		}
	}
	return l
}

func authorNameLF(name string) string {
	var out []string
	for _, a := range splitAuthors(name) {
		parts := strings.Fields(a)
		if len(parts) < 2 {
			out = append(out, a)
			continue
		}
		out = append(out, parts[len(parts)-1]+", "+strings.Join(parts[:len(parts)-1], " "))
	}
	return strings.Join(out, ", ")
}

func yearString(y int) any {
	if y <= 0 {
		return nil
	}
	return itoa(int64(y))
}

func nilIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilChapters(v []Chapter) []Chapter {
	if v == nil {
		return []Chapter{}
	}
	return v
}
