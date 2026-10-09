//go:build !linux

package main

// becomeDataOwner has nothing to do off Linux: the image is Linux, and a development
// build on Windows or macOS runs as its own user.
func becomeDataOwner(string) error { return nil }
