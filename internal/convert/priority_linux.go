//go:build linux

package convert

import (
	"os/exec"
	"syscall"
)

// lowPriority puts an encode in its own process group at the lowest scheduling priority, so
// Plex, game servers and anything else interactive win CPU contention automatically.
//
// Encoding is bulk background work: it should soak up idle capacity and yield instantly when
// something else needs the machine. Without this, a CPU encode competes on equal terms with
// everything else on the box, which is why converting a large library on a shared server was
// previously a bad idea regardless of how many cores it was given.
//
// Setpgid means the niceness applies to ffmpeg and any child it spawns, and it also lets the
// whole group be signalled together on cancellation.
func lowPriority(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// applyNice lowers the priority of an already-started process group. Called after Start
// because the pid isn't known before then; a failure here is not fatal — the encode simply
// runs at normal priority.
func applyNice(pid, nice int) {
	_ = syscall.Setpriority(syscall.PRIO_PGRP, pid, nice)
}

// suspendGroup freezes an encode's whole process group (ffmpeg and anything it spawned)
// with SIGSTOP. The encode keeps its place: SIGCONT resumes it exactly where it stopped,
// which is how a job started inside the encode hours waits out the day instead of either
// running into prime time or being thrown away and redone.
func suspendGroup(pid int) error { return syscall.Kill(-pid, syscall.SIGSTOP) }

// resumeGroup continues a group frozen by suspendGroup.
func resumeGroup(pid int) error { return syscall.Kill(-pid, syscall.SIGCONT) }

// canSuspend reports whether running encodes can be paused on this platform.
const canSuspend = true
