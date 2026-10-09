package scheduler

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/jobs"
)

// JobKind is the job kind a Run now is recorded under; the target is the task's name.
const JobKind = "task"

// Submitter is the job runner, narrowed to what Run now needs.
type Submitter interface {
	Submit(ctx context.Context, spec jobs.Spec) (int64, bool, error)
}

// ViaJobs makes Run now a job on r: kind "task", target the task's name, in the task
// class. The job's message is what the run said (e.g. that it stood down for a tick
// already running), and a failed or panicked run fails the job with the same error.
func ViaJobs(ctx context.Context, r Submitter) JobSubmitter {
	return func(name, trigger string, run func(ctx context.Context, jobID int64, setMessage func(string)) error) (int64, bool, error) {
		return r.Submit(ctx, jobs.Spec{
			Kind: JobKind, Target: name, Trigger: trigger, Class: jobs.ClassTask,
			Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
				return nil, run(ctx, p.JobID(), p.SetMessage)
			},
		})
	}
}
