package cleanups

import (
	"context"
	"errors"
	"time"
)

type Composite struct {
	cleanups []func(ctx context.Context) error
}

// Add adds a cleanup to be called.
func (c *Composite) Add(f func(ctx context.Context) error) {
	c.cleanups = append(c.cleanups, f)
}

// Call calls all cleanups in reverse order and returns an error combining all
// non-nil errors.
// Context is passed to the cleanups, but its cancellation is disallowed and only
// values passed via WithValue are retained.
func (c *Composite) Call(ctx context.Context) error {
	err := call(ctx, c.cleanups)
	c.cleanups = nil
	return err
}

// Release removes all cleanups, turning Call into a no-op.
// Caller still can call the cleanups by calling the returned function
// which is equivalent to calling the Call before Release was called.
func (c *Composite) Release() func(context.Context) error {
	cleanups := c.cleanups
	c.cleanups = nil
	return func(ctx context.Context) error {
		return call(ctx, cleanups)
	}
}

func call(ctx context.Context, cleanups []func(ctx context.Context) error) error {
	var errs error
	// Disallow cancellation, but pass all values.
	// TODO: Replace with context.WithoutCancel in go1.21
	ctx = notCancellable{parent: ctx}

	for idx := len(cleanups) - 1; idx >= 0; idx-- {
		c := cleanups[idx]
		errs = errors.Join(errs, c(ctx))
	}
	return errs
}

type notCancellable struct {
	parent context.Context
}

func (notCancellable) Deadline() (deadline time.Time, ok bool) {
	return
}

func (notCancellable) Done() <-chan struct{} {
	return nil
}

func (e notCancellable) Err() error {
	return nil
}

func (e notCancellable) Value(key any) any {
	return e.parent.Value(key)
}
