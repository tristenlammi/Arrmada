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
