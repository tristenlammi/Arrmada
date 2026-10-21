package issues

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/adultfilter"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/store"
)

// TopicUpdated tells open Issues tabs to reload: staff-only on the websocket (a plain
// topic), with the issue's id and status — never a title or a name. Each reporter hears
// about their own on user.<id>.issue.updated.
const TopicUpdated = "issue.updated"

// Notice is one message to the people who reported an issue.
type Notice struct {
	Title, Body, Ref, Kind, MediaType string
}

// Notifier tells requesters something through their own channels (inbox, personal Apprise,
// Web Push): requests.Service.NotifyUsers, adapted in main. Idempotent per (user, Ref).
type Notifier func(ctx context.Context, userIDs []int64, n Notice) (int, error)

// StaffAlert tells staff a new issue was reported (the "Problem reported" alert, their
// inboxes and Web Push): requests.Service.AlertStaff, adapted in main.
type StaffAlert func(ctx context.Context, i Issue, data map[string]any)

// Publisher is the event bus, as far as issues needs it.
type Publisher interface {
	Publish(topic string, data any)
}

// Service records issues and resolves them.
type Service struct {
	db   store.Pool
	lib  Library
	log  *slog.Logger
	now  func() time.Time
	bus  Publisher
	tell Notifier
	warn StaffAlert
	subs SubtitleJobs
	kick func()
}

// NewService builds the service. lib reads the library; the other parts are wired with the
// setters and may stay unset (tests).
func NewService(db store.Pool, lib Library, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: db, lib: lib, log: log, now: time.Now}
}

// SetPublisher wires the event bus (live reloads of open Issues tabs).
func (s *Service) SetPublisher(p Publisher) { s.bus = p }

// SetNotifier wires how reporters are told their issue was closed.
func (s *Service) SetNotifier(n Notifier) { s.tell = n }

// SetStaffAlert wires how staff hear about a new issue.
func (s *Service) SetStaffAlert(a StaffAlert) { s.warn = a }

// SetAttentionKick wires the Needs-you feed's refresh, kicked when an issue opens or closes.
func (s *Service) SetAttentionKick(fn func()) { s.kick = fn }

// Create records a report. The item must be in the library with a file, and its title must
// pass the adult filter. A report of a problem already open for the same item and category
// joins that issue (joined = true) instead of opening a duplicate; only a new issue alerts
// staff.
func (s *Service) Create(ctx context.Context, r Report) (Issue, bool, error) {
	r.Note = strings.TrimSpace(r.Note)
	if !validCategory(r.Category) {
		return Issue{}, false, invalid("pick what's wrong: %s", strings.Join(Categories, ", "))
	}
	if utf8.RuneCountInString(r.Note) > NoteMax {
		return Issue{}, false, invalid("the note can be at most %d characters", NoteMax)
	}
	if r.UserID <= 0 {
		return Issue{}, false, invalid("sign in first")
	}
	switch r.MediaType {
	case MediaMovie:
		r.Season, r.Episode = 0, 0
	case MediaSeries:
		if r.Season < 0 || r.Episode < 1 {
			return Issue{}, false, invalid("pick the season and episode")
		}
	default:
		return Issue{}, false, invalid("media_type must be movie or series")
	}
	if r.TMDBID <= 0 {
		return Issue{}, false, ErrNotInLibrary
	}
	it, err := s.lib.Find(ctx, r.MediaType, r.TMDBID, r.Season, r.Episode)
	if err != nil {
		return Issue{}, false, err
	}
	// The adult filter is always on: a title it matches is never offered to a requester,
	// so it can't be reported either — and the answer reads like any title not in the
	// library.
	if adultfilter.Matches(it.Title) {
		return Issue{}, false, ErrNotInLibrary
	}
	if it.Path == "" {
		return Issue{}, false, ErrNoFile
	}
	now := s.now().Unix()
	t := Target{MediaType: r.MediaType, LibraryID: it.LibraryID, Season: r.Season, Episode: r.Episode}
	var out Issue
	joined := false
	err = store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		// The transaction holds the write lock from its start (BEGIN IMMEDIATE), so two
		// people reporting at once can't both miss the live issue and open two.
		live, ok, err := liveIssue(ctx, tx, t, r.Category)
		if err != nil {
			return err
		}
		id := live.ID
		if ok {
			joined = true
		} else {
			id, err = insertIssue(ctx, tx, Issue{
				MediaType: r.MediaType, LibraryID: it.LibraryID, TMDBID: r.TMDBID, Title: it.Title, Year: it.Year,
				PosterURL: it.PosterURL, Season: r.Season, Episode: r.Episode, Category: r.Category, Note: r.Note,
				ReportedBy: r.UserID, ReportedByName: r.UserName, CreatedAt: now,
			})
			if err != nil {
				return err
			}
		}
		if err := addReporter(ctx, tx, id, r.UserID, r.UserName, r.Note, now); err != nil {
			return err
		}
		out, err = getIssue(ctx, tx, id)
		return err
	})
	if err != nil {
		return Issue{}, false, err
	}
	if err := s.fill(ctx, []*Issue{&out}); err != nil {
		s.log.Warn("issues: couldn't read the reporters", "issue", out.ID, "err", err)
	}
	s.changed(out)
	if !joined {
		s.alertStaff(ctx, out)
	}
	s.log.Info("issues: problem reported", "issue", out.ID, "media", out.MediaType, "library_id", out.LibraryID,
		"category", out.Category, "joined", joined)
	return out, joined, nil
}

// alertStaff sends the "Problem reported" alert for a new issue.
func (s *Service) alertStaff(ctx context.Context, i Issue) {
	if s.warn == nil {
		return
	}
	s.warn(ctx, i, map[string]any{
		"id": i.ID, "media_type": i.MediaType, "tmdb_id": i.TMDBID, "title": i.Title, "year": i.Year,
		"season": i.Season, "episode": i.Episode, "category": i.Category, "note": i.Note,
		"reported_by_name": notify.PersonName(i.ReportedByName), // never an email: it leaves the app
	})
}

// StaffAlertDedupe is the dedupe key a new issue's staff alert is queued under: one alert
// per issue, however often anything retries.
func StaffAlertDedupe(id int64) string { return fmt.Sprintf("%s:%d", notify.EventIssueReported, id) }

// List returns one page of issues (newest first), how many match in all, and the open and
// closed counts for the same viewer. Staff pass UserID 0 for everyone's.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Issue, int, Counts, error) {
	list, total, err := list(ctx, s.db, f)
	if err != nil {
		return nil, 0, Counts{}, err
	}
	c, err := counts(ctx, s.db, f.UserID)
	if err != nil {
		return nil, 0, Counts{}, err
	}
	ptrs := make([]*Issue, len(list))
	for k := range list {
		ptrs[k] = &list[k]
	}
	if err := s.fill(ctx, ptrs); err != nil {
		return nil, 0, Counts{}, err
	}
	return list, total, c, nil
}

// Get returns one issue with its reporters.
func (s *Service) Get(ctx context.Context, id int64) (Issue, error) {
	i, err := getIssue(ctx, s.db, id)
	if err != nil {
		return Issue{}, err
	}
	err = s.fill(ctx, []*Issue{&i})
	return i, err
}

// IsReporter reports whether userID reported issue id (the requester's view of one issue).
func (s *Service) IsReporter(ctx context.Context, id, userID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_reporters WHERE issue_id = ? AND user_id = ?`, id, userID).Scan(&n)
	return n > 0, err
}

// Open lists the open and fixing issues, oldest first, for the Needs-you feed.
func (s *Service) Open(ctx context.Context, limit int) ([]Issue, error) {
	return queryIssues(ctx, s.db, `SELECT `+issueCols+` FROM issues WHERE status IN ('open', 'fixing') ORDER BY created_at, id LIMIT ?`, limit)
}

// Episodes lists the episodes of a show (by TMDB id) that have a file, for the report
// sheet. A show the adult filter matches reads as not in the library.
func (s *Service) Episodes(ctx context.Context, tmdbID int) ([]SeasonFiles, error) {
	title, out, err := s.lib.EpisodesWithFiles(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	if adultfilter.Matches(title) {
		return nil, ErrNotInLibrary
	}
	return out, nil
}

// fill attaches each issue's reporters and their count.
func (s *Service) fill(ctx context.Context, list []*Issue) error {
	ids := make([]int64, 0, len(list))
	for _, i := range list {
		ids = append(ids, i.ID)
	}
	by, err := reportersFor(ctx, s.db, ids)
	if err != nil {
		return err
	}
	for _, i := range list {
		i.Reporters = by[i.ID]
		i.ReporterCount = len(i.Reporters)
	}
	return nil
}

// Resolve closes an issue as fixed and tells everyone who reported it, with the note when
// staff gave one.
func (s *Service) Resolve(ctx context.Context, id int64, resolution string, by int64, byName string) (Issue, error) {
	return s.close(ctx, id, StatusResolved, resolution, by, byName)
}

// Dismiss closes an issue without a fix (not a real problem, can't be fixed) and tells the
// reporters, with the reason when staff gave one.
func (s *Service) Dismiss(ctx context.Context, id int64, reason string, by int64, byName string) (Issue, error) {
	return s.close(ctx, id, StatusDismissed, reason, by, byName)
}

func (s *Service) close(ctx context.Context, id int64, status, note string, by int64, byName string) (Issue, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > ResolutionMax {
		return Issue{}, invalid("the note can be at most %d characters", ResolutionMax)
	}
	i, err := getIssue(ctx, s.db, id)
	if err != nil {
		return Issue{}, err
	}
	if i.Status != StatusOpen && i.Status != StatusFixing {
		return Issue{}, ErrClosed
	}
	ok, err := closeIssue(ctx, s.db, id, status, note, by, byName, s.now().Unix())
	if err != nil {
		return Issue{}, err
	}
	if !ok {
		return Issue{}, ErrClosed
	}
	i, err = s.Get(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	s.changed(i)
	s.tellReporters(ctx, i)
	s.log.Info("issues: closed", "issue", id, "status", status, "by", by)
	return i, nil
}

// tellReporters tells everyone who reported a closed issue what became of it, once each
// (the inbox reference is per issue and outcome), and stamps told_at once all have been
// told. A failure leaves it unstamped and Sweep tries again.
func (s *Service) tellReporters(ctx context.Context, i Issue) {
	if s.tell == nil || (i.Status != StatusResolved && i.Status != StatusDismissed) {
		return
	}
	reps := i.Reporters
	if reps == nil {
		var err error
		if reps, err = reporters(ctx, s.db, i.ID); err != nil {
			s.log.Warn("issues: couldn't list the reporters to tell", "issue", i.ID, "err", err)
			return
		}
	}
	ids := make([]int64, 0, len(reps))
	for _, r := range reps {
		ids = append(ids, r.UserID)
	}
	if _, err := s.tell(ctx, ids, closedNotice(i)); err != nil {
		s.log.Warn("issues: couldn't tell everyone who reported it", "issue", i.ID, "err", err)
		return
	}
	if err := markTold(ctx, s.db, i.ID, s.now().Unix()); err != nil {
		s.log.Warn("issues: couldn't record that the reporters were told", "issue", i.ID, "err", err)
	}
}

// closedNotice is what reporters are told about a closed issue. Its reference leads to the
// title's own page ("movie:603:issue:12:resolved" opens /discover/movie/603), where they
// can watch it again.
func closedNotice(i Issue) Notice {
	w := what(i.Title, i.Season, i.Episode)
	n := Notice{MediaType: i.MediaType, Ref: fmt.Sprintf("%s:%d:issue:%d:%s", i.MediaType, i.TMDBID, i.ID, i.Status)}
	if i.TMDBID <= 0 {
		n.Ref = fmt.Sprintf("issue:%d:%s", i.ID, i.Status)
	}
	if i.Status == StatusResolved {
		n.Title, n.Kind = "Problem fixed", "issue-resolved"
		n.Body = fmt.Sprintf("The problem you reported with %s is fixed.", w)
	} else {
		n.Title, n.Kind = "Problem report closed", "issue-dismissed"
		n.Body = fmt.Sprintf("We looked at the problem you reported with %s and closed it.", w)
	}
	if i.Resolution != "" {
		n.Body += " " + i.Resolution
	}
	return n
}

// changed lets open pages reload and the Needs-you feed recount.
func (s *Service) changed(i Issue) {
	if s.bus != nil {
		data := map[string]any{"id": i.ID, "status": i.Status}
		s.bus.Publish(TopicUpdated, data)
		seen := map[int64]bool{}
		for _, r := range i.Reporters {
			if !seen[r.UserID] {
				seen[r.UserID] = true
				s.bus.Publish(eventbus.UserTopic(r.UserID, TopicUpdated), data)
			}
		}
	}
	if s.kick != nil {
		s.kick()
	}
}

// toldRetryFor is how long Sweep keeps retrying a closed issue's notices: past a week the
// news is stale, and an inbox that has been failing that long has bigger problems.
const toldRetryFor = 7 * 24 * time.Hour

// Sweep is the backstop. It settles the issues being fixed (checkFixing: a finished
// subtitle job, a new file whose import signal was missed, a fix that brought nothing),
// then re-sends the notices for closed issues whose reporters weren't all told (an inbox
// write failed). Each reporter hears once, whatever retries.
func (s *Service) Sweep(ctx context.Context) error {
	if err := s.checkFixing(ctx); err != nil {
		return err
	}
	list, err := untold(ctx, s.db, s.now().Add(-toldRetryFor).Unix())
	if err != nil {
		return err
	}
	for _, i := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.tellReporters(ctx, i)
	}
	return nil
}
