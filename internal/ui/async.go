package ui

import "sync"

// bgLoad runs one slow read (SQLite, a transcript on disk) off the Qt
// thread. The goroutine only writes its result into mutex-guarded state;
// the page's tick picks it up with take and updates widgets there, so no
// goroutine ever touches a widget. At most one load runs at a time: a page
// that wants fresher data asks again after the tick has taken the result.
type bgLoad[T any] struct {
	mu   sync.Mutex
	busy bool
	have bool
	gen  uint64
	val  T
	err  error
}

// start runs fn in a goroutine unless a load is already running, and
// reports whether it did. gen tags the request so that take can drop a
// result whose inputs (a filter, a selection) changed while it ran.
func (b *bgLoad[T]) start(gen uint64, fn func() (T, error)) bool {
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return false
	}
	b.busy = true
	b.mu.Unlock()
	go func() {
		v, err := fn()
		b.mu.Lock()
		b.val, b.err, b.gen, b.have, b.busy = v, err, gen, true, false
		b.mu.Unlock()
	}()
	return true
}

// running reports whether a load is in flight.
func (b *bgLoad[T]) running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.busy
}

// take returns a finished result once. A result tagged with another gen than
// want is discarded.
func (b *bgLoad[T]) take(want uint64) (v T, err error, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.have {
		return v, nil, false
	}
	v, err, gen := b.val, b.err, b.gen
	var zero T
	b.val, b.err, b.have = zero, nil, false
	if gen != want {
		return v, nil, false
	}
	return v, err, true
}
