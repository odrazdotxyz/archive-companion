package archive

import (
	"context"
	"sync"
)

// Operations on one file of one drive take turns, across every Archive value of
// the process: Archive, Restore and Delete decide from the same record whether
// the file or its copy may go, and doing so at once can remove both.
var locks = struct {
	sync.Mutex
	held map[string]*fileLock
}{held: map[string]*fileLock{}}

type fileLock struct {
	free  chan struct{} // holds one value while nobody has the lock
	users int
}

// lock waits for the file at path of the drive and returns how to release it.
func (a *Archive) lock(ctx context.Context, path string) (unlock func(), err error) {
	key := a.src.DriveKey + "\x00" + path
	locks.Lock()
	l := locks.held[key]
	if l == nil {
		l = &fileLock{free: make(chan struct{}, 1)}
		l.free <- struct{}{}
		locks.held[key] = l
	}
	l.users++
	locks.Unlock()
	drop := func() {
		locks.Lock()
		if l.users--; l.users == 0 {
			delete(locks.held, key)
		}
		locks.Unlock()
	}
	select {
	case <-l.free:
		return func() { l.free <- struct{}{}; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
