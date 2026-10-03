//go:build !unix

package queue

import "os"

// Without flock, the queue is not protected against two t processes
// writing at once. beacon targets Linux and FreeBSD, so this is only here
// to keep the package building elsewhere.

func lock(f *os.File) error   { return nil }
func unlock(f *os.File) error { return nil }
