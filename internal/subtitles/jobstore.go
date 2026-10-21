package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// jobStore keeps the queue and its recent history in subtitle_jobs, so an update or a
// restart doesn't drop what was waiting (SUB-23). The in-memory queue (Service.pending,
// active, jobs) stays the working copy the worker reads; every state change is written
// through here. Stage and progress ticks are never written.
//
// A nil store, or one without a database, keeps nothing: the queue works in memory alone
// (the unit tests that need no database).
type jobStore struct {
	db store.Pool
	// mu orders the writes. A caller takes it before releasing Service.mu (see
	// Service.unlockAndPersist), so rows change in the same order the queue did, without
	// holding the queue's lock over disk I/O.
	mu sync.Mutex
}

// jobHistoryKeep is how many finished jobs the table keeps; jobMemoryKeep how many of
// them the in-memory list (what the Queue tab polls) carries.
const (
	jobHistoryKeep = 500
	jobMemoryKeep  = 200
)

// interruptedNote is the note a job still running when the app stopped gets on the way
// back into the queue.
const interruptedNote = "interrupted by restart"

func newJobStore(db store.Pool) *jobStore { return &jobStore{db: db} }

func (st *jobStore) ok() bool { return st != nil && st.db != nil }

const jobCols = `id, media_key, kind, movie_id, series_id, season, episode, title, priority, lane, state,
	langs, redo, note, run_id, outcomes_json, audio_sec, queued_at, started_at, finished_at,
	mode, repair_langs, retire_json, audio_stream, track_index, pick_file_id`

// jobArgs is a job's row in jobCols order.
func jobArgs(j Job) ([]any, error) {
	var outcomes any
	if len(j.Outcomes) > 0 {
		b, err := json.Marshal(j.Outcomes)
		if err != nil {
			return nil, err
		}
		outcomes = string(b)
	}
	var retire any
	if len(j.retire) > 0 {
		rs := make([]savedRetire, len(j.retire))
		for i, r := range j.retire {
			rs[i] = savedRetire(r)
		}
		b, err := json.Marshal(rs)
		if err != nil {
			return nil, err
		}
		retire = string(b)
	}
	return []any{j.ID, j.key(), j.Kind, nullID(j.MovieID), nullID(j.SeriesID), nullInt(j.Season, j.Kind == "episode"),
		nullInt(j.Episode, j.Kind == "episode"), j.Title, j.Priority, j.lane(), string(j.State),
		strings.Join(j.Langs, ","), boolInt(j.Redo), nullStr(j.Note), nullStr(j.RunID), outcomes, j.AudioSec,
		j.At, nullTime(j.StartedAt), nullTime(j.FinishedAt),
		j.Mode, strings.Join(j.repairLangs, ","), retire, nullIntPtr(j.AudioStream), nullIntPtr(j.TrackIndex), nullStr(j.PickFileID)}, nil
}

// savedRetire is an orphanRetire as the table stores it.
type savedRetire struct {
	Name string `json:"name"`
	Lang string `json:"lang"`
}

func nullInt(n int, valid bool) any {
	if !valid {
		return nil
	}
	return n
}

func nullTime(unix int64) any {
	if unix == 0 {
		return nil
	}
	return unix
}

func scanJob(sc interface{ Scan(...any) error }) (Job, error) {
	var (
		j                                   Job
		movieID, seriesID, season, episode  sql.NullInt64
		langs, note, runID, outcomes, state sql.NullString
		repairLangs, retire                 sql.NullString
		startedAt, finishedAt               sql.NullInt64
		audioStream, trackIndex             sql.NullInt64
		pickFileID                          sql.NullString
		redo                                int
		mediaKey                            string
	)
	if err := sc.Scan(&j.ID, &mediaKey, &j.Kind, &movieID, &seriesID, &season, &episode, &j.Title, &j.Priority,
		&j.Lane, &state, &langs, &redo, &note, &runID, &outcomes, &j.AudioSec, &j.At, &startedAt, &finishedAt,
		&j.Mode, &repairLangs, &retire, &audioStream, &trackIndex, &pickFileID); err != nil {
		return Job{}, err
	}
	j.MovieID, j.SeriesID = movieID.Int64, seriesID.Int64
	j.Season, j.Episode = int(season.Int64), int(episode.Int64)
	j.State, j.Redo, j.Note, j.RunID = JobState(state.String), redo != 0, note.String, runID.String
	j.StartedAt, j.FinishedAt = startedAt.Int64, finishedAt.Int64
	j.AudioStream, j.TrackIndex, j.PickFileID = intPtr(audioStream), intPtr(trackIndex), pickFileID.String
	if langs.String != "" {
		j.Langs = strings.Split(langs.String, ",")
	}
	if outcomes.String != "" {
		if err := json.Unmarshal([]byte(outcomes.String), &j.Outcomes); err != nil {
			return Job{}, fmt.Errorf("job %d outcomes: %w", j.ID, err)
		}
	}
	if repairLangs.String != "" {
		j.repairLangs = strings.Split(repairLangs.String, ",")
	}
	if retire.String != "" {
		var rs []savedRetire
		if err := json.Unmarshal([]byte(retire.String), &rs); err != nil {
			return Job{}, fmt.Errorf("job %d retire list: %w", j.ID, err)
		}
		for _, r := range rs {
			j.retire = append(j.retire, orphanRetire(r))
		}
	}
	return j, nil
}

// write inserts new jobs and saves changed ones, in one transaction: a sweep's thousands
// of rows cost one commit, not one each. A new job whose file and lane already has a live
// row (one the in-memory queue somehow doesn't know) is left out rather than failing the
// batch; the returned count says how many were.
func (st *jobStore) write(ctx context.Context, inserts, saves []Job) (skipped int, err error) {
	if !st.ok() || len(inserts)+len(saves) == 0 {
		return 0, nil
	}
	err = store.WithTx(ctx, st.db, func(tx *store.Transaction) error {
		skipped = 0
		if len(inserts) > 0 {
			ins, err := tx.PrepareContext(ctx, `INSERT INTO subtitle_jobs (`+jobCols+`)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`)
			if err != nil {
				return err
			}
			defer ins.Close()
			for _, j := range inserts {
				args, err := jobArgs(j)
				if err != nil {
					return err
				}
				res, err := ins.ExecContext(ctx, args...)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 0 {
					skipped++
				}
			}
		}
		for _, j := range saves {
			if err := saveJob(ctx, tx, j); err != nil {
				return err
			}
		}
		return nil
	})
	return skipped, err
}

// saveJob writes every column a job can change after it was queued.
func saveJob(ctx context.Context, db store.Execer, j Job) error {
	args, err := jobArgs(j)
	if err != nil {
		return err
	}
	// args[0] is the id; the rest follow jobCols. media_key, kind, mode and the ids never change.
	_, err = db.ExecContext(ctx, `UPDATE subtitle_jobs SET title = ?, priority = ?, lane = ?, state = ?, langs = ?,
		redo = ?, note = ?, run_id = ?, outcomes_json = ?, audio_sec = ?, started_at = ?, finished_at = ?, repair_langs = ?, retire_json = ? WHERE id = ?`,
		args[7], args[8], args[9], args[10], args[11], args[12], args[13], args[14], args[15], args[16], args[18], args[19], args[21], args[22], j.ID)
	return err
}

// lastID is the highest id ever handed out, so the next one is never a repeat — even of
// a row since pruned (SQLite AUTOINCREMENT, or PostgreSQL's explicit-id sequence
// high-water trigger, keeps it after the row has gone).
func (st *jobStore) lastID(ctx context.Context) (int64, error) {
	var seq, top sql.NullInt64
	if err := st.db.QueryRowContext(ctx, `SELECT MAX(id) FROM subtitle_jobs`).Scan(&top); err != nil {
		return 0, err
	}
	sequenceQuery := `SELECT seq FROM sqlite_sequence WHERE name = 'subtitle_jobs'`
	if store.DialectOf(st.db) == store.Postgres {
		sequenceQuery = `SELECT pg_catalog.pg_sequence_last_value(
			pg_catalog.pg_get_serial_sequence('subtitle_jobs', 'id')::regclass)`
	}
	err := st.db.QueryRowContext(ctx, sequenceQuery).Scan(&seq)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return max(seq.Int64, top.Int64), nil
}

// restore is the queue as the last run left it. Jobs still running then go back to queued
// (their note says so) and come back with the rest, most urgent line first and oldest
// first within it; recent is the newest finished jobs, for the history.
func (st *jobStore) restore(ctx context.Context, recentKeep int) (queued, recent []Job, last int64, err error) {
	if _, err = st.db.ExecContext(ctx, `UPDATE subtitle_jobs SET state = ?, note = ?, started_at = NULL WHERE state = ?`,
		string(StateQueued), interruptedNote, string(StateRunning)); err != nil {
		return nil, nil, 0, err
	}
	if queued, err = st.query(ctx, `SELECT `+jobCols+` FROM subtitle_jobs WHERE state = ? ORDER BY priority, queued_at, id`, string(StateQueued)); err != nil {
		return nil, nil, 0, err
	}
	if recent, err = st.query(ctx, `SELECT `+jobCols+` FROM subtitle_jobs WHERE state NOT IN (?, ?) ORDER BY id DESC LIMIT ?`,
		string(StateQueued), string(StateRunning), recentKeep); err != nil {
		return nil, nil, 0, err
	}
	last, err = st.lastID(ctx)
	return queued, recent, last, err
}

func (st *jobStore) query(ctx context.Context, q string, args ...any) ([]Job, error) {
	rows, err := st.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobQuery pages the job history (GET /subtitles/jobs?before=&state=&limit=), newest first.
type JobQuery struct {
	Before int64    // only jobs with a smaller id; 0 = from the newest
	State  JobState // only this state; "" = any
	Limit  int      // page size (default 50, at most 500)
}

func (q JobQuery) limit() int {
	switch {
	case q.Limit <= 0:
		return 50
	case q.Limit > 500:
		return 500
	}
	return q.Limit
}

// page reads one page of the history.
func (st *jobStore) page(ctx context.Context, q JobQuery) ([]Job, error) {
	where, args := []string{"1 = 1"}, []any{}
	if q.Before > 0 {
		where, args = append(where, "id < ?"), append(args, q.Before)
	}
	if q.State != "" {
		where, args = append(where, "state = ?"), append(args, string(q.State))
	}
	args = append(args, q.limit())
	return st.query(ctx, `SELECT `+jobCols+` FROM subtitle_jobs WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
}

// get reads one job by id.
func (st *jobStore) get(ctx context.Context, id int64) (Job, bool, error) {
	j, err := scanJob(st.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM subtitle_jobs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Job{}, false, nil
	}
	return j, err == nil, err
}

// prune keeps the newest jobHistoryKeep finished jobs; queued and running ones always stay.
func (st *jobStore) prune(ctx context.Context) (int64, error) {
	if !st.ok() {
		return 0, nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	res, err := st.db.ExecContext(ctx, `DELETE FROM subtitle_jobs WHERE state NOT IN (?, ?) AND id NOT IN (
		SELECT id FROM subtitle_jobs WHERE state NOT IN (?, ?) ORDER BY id DESC LIMIT ?)`,
		string(StateQueued), string(StateRunning), string(StateQueued), string(StateRunning), jobHistoryKeep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// unlockAndPersist releases s.mu and runs write against the store, in queue order: the
// store's write lock is taken before s.mu is let go, so two changes made one after the
// other under s.mu reach the table in that order too. Call it with s.mu held, in place of
// s.mu.Unlock(). A write error is logged: the queue carries on in memory, and the next
// restart just knows less.
func (s *Service) unlockAndPersist(what string, write func(ctx context.Context, st *jobStore) error) {
	st := s.jobsDB
	if !st.ok() {
		s.mu.Unlock()
		return
	}
	st.mu.Lock()
	s.mu.Unlock()
	defer st.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := write(ctx, st); err != nil {
		s.log.Warn("subtitles: could not save the queue", "change", what, "err", err)
	}
}

// saveLocked is unlockAndPersist for jobs whose rows just changed. Call with s.mu held.
func (s *Service) saveLocked(what string, jobs ...*Job) {
	snaps := make([]Job, len(jobs))
	for i, j := range jobs {
		snaps[i] = *j
	}
	s.unlockAndPersist(what, func(ctx context.Context, st *jobStore) error {
		_, err := st.write(ctx, nil, snaps)
		return err
	})
}

// loadQueue puts back what the last run left queued (SUB-23): jobs that were running go
// back in line marked "interrupted by restart", and the newest finished jobs come back as
// history. Ids carry on from the highest ever used. NewService calls it, before anything
// can be enqueued.
func (s *Service) loadQueue(ctx context.Context) error {
	if !s.jobsDB.ok() {
		return nil
	}
	queued, recent, last, err := s.jobsDB.restore(ctx, jobMemoryKeep)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = map[string]*Job{}
	}
	s.nextID = max(s.nextID, last)
	list := make([]*Job, 0, len(queued)+len(recent))
	for i := range queued {
		j := &queued[i]
		j.Priority = clampPrio(j.Priority)
		s.active[j.activeKey()] = j
		s.pushLocked(j)
		list = append(list, j)
	}
	for i := range recent {
		list = append(list, &recent[i])
	}
	// Newest first, like the list enqueue builds.
	sortJobsNewestFirst(list)
	s.jobs = append(list, s.jobs...)
	return nil
}
