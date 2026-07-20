//go:build !linux

package convert

import "os/exec"

// lowPriority is a no-op off Linux — the deployment target is a Linux container, and the
// dev platforms don't need encodes de-prioritized.
func lowPriority(cmd *exec.Cmd) {}

// applyNice is a no-op off Linux.
func applyNice(pid, nice int) {}
