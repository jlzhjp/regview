package ttlcache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type resource struct {
	weight int64
	closed atomic.Int64
}

func testCache(t *testing.T, entries int, weight int64) *Cache[string, *resource] {
	t.Helper()
	c, err := New[string](Options[*resource]{
		TTL: time.Second, LoadTimeout: time.Minute, MaxEntries: entries, MaxWeight: weight,
		Weight:  func(r *resource) int64 { return r.weight },
		Dispose: func(r *resource) error { r.closed.Add(1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func acquire(t *testing.T, c *Cache[string, *resource], key string, r *resource) *Lease[*resource] {
	t.Helper()
	l, err := c.Acquire(t.Context(), key, func(context.Context) (*resource, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestIndependentLeasesAndShutdown(t *testing.T) {
	c := testCache(t, 2, 10)
	r := &resource{weight: 1}
	a := acquire(t, c, "key", r)
	b := acquire(t, c, "key", &resource{weight: 1})
	if a.Value() != b.Value() {
		t.Fatal("cache hit reloaded value")
	}
	_ = a.Close()
	_ = a.Close()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if r.closed.Load() != 0 {
		t.Fatal("closed an active lease")
	}
	if _, err := c.Acquire(t.Context(), "key", nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	_ = b.Close()
	_ = b.Close()
	if r.closed.Load() != 1 {
		t.Fatal("last release did not dispose exactly once")
	}
}

func TestAbsoluteTTLAndActiveReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testCache(t, 2, 10)
		old, replacement := &resource{weight: 1}, &resource{weight: 1}
		active := acquire(t, c, "key", old)
		time.Sleep(750 * time.Millisecond)
		hit := acquire(t, c, "key", replacement)
		_ = hit.Close()
		time.Sleep(500 * time.Millisecond)
		fresh := acquire(t, c, "key", replacement)
		if fresh.Value() != replacement {
			t.Fatal("hit extended TTL or expired entry was reused")
		}
		if old.closed.Load() != 0 {
			t.Fatal("expiry closed active reader")
		}
		_ = active.Close()
		if old.closed.Load() != 1 {
			t.Fatal("expired reader retained after last release")
		}
		_ = fresh.Close()
		_ = c.Close()
	})
}

func TestIdleEntryClosesWithoutAnotherRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testCache(t, 2, 10)
		r := &resource{weight: 1}
		l := acquire(t, c, "key", r)
		_ = l.Close()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if r.closed.Load() != 1 {
			t.Fatal("TTL did not release idle resource")
		}
		_ = c.Close()
	})
}

func TestLRUAndByteLimits(t *testing.T) {
	for _, limits := range []struct {
		entries int
		weight  int64
	}{{2, 100}, {100, 6}} {
		c := testCache(t, limits.entries, limits.weight)
		a, b, d := &resource{weight: 3}, &resource{weight: 3}, &resource{weight: 3}
		x := acquire(t, c, "a", a)
		_ = x.Close()
		x = acquire(t, c, "b", b)
		_ = x.Close()
		x = acquire(t, c, "a", a)
		_ = x.Close()
		x = acquire(t, c, "c", d)
		_ = x.Close()
		if b.closed.Load() != 1 || a.closed.Load() != 0 || d.closed.Load() != 0 {
			t.Fatal("capacity did not evict least recently used value")
		}
		_ = c.Close()
	}
}

func TestOversizedValuesAreNotRetained(t *testing.T) {
	c := testCache(t, 2, 2)
	r := &resource{weight: 3}
	l := acquire(t, c, "key", r)
	if r.closed.Load() != 0 {
		t.Fatal("oversized value closed before use")
	}
	_ = l.Close()
	if r.closed.Load() != 1 {
		t.Fatal("oversized value retained")
	}
	next := &resource{weight: 3}
	l = acquire(t, c, "key", next)
	if l.Value() != next {
		t.Fatal("oversized value cached")
	}
	_ = l.Close()
}

func TestCoalescedLoadSurvivesFirstWaiterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testCache(t, 2, 10)
		r := &resource{weight: 1}
		var calls atomic.Int64
		started, proceed := make(chan context.Context, 1), make(chan struct{})
		load := func(ctx context.Context) (*resource, error) { calls.Add(1); started <- ctx; <-proceed; return r, nil }
		ctx, cancel := context.WithCancel(t.Context())
		first := make(chan error, 1)
		go func() { _, err := c.Acquire(ctx, "key", load); first <- err }()
		loadCtx := <-started
		second := make(chan *Lease[*resource], 1)
		go func() {
			l, err := c.Acquire(t.Context(), "key", load)
			if err != nil {
				t.Error(err)
			}
			second <- l
		}()
		synctest.Wait()
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if loadCtx.Err() != nil {
			t.Fatal("one waiter canceled a shared load")
		}
		close(proceed)
		l := <-second
		if l == nil || l.Value() != r || calls.Load() != 1 {
			t.Fatal("load was not coalesced")
		}
		synctest.Wait()
		if r.closed.Load() != 0 {
			t.Fatal("load context cancellation closed published resource")
		}
		_ = l.Close()
		_ = c.Close()
	})
}

func TestCanceledLoadCannotReplaceNewerLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testCache(t, 2, 10)
		old, fresh := &resource{weight: 1}, &resource{weight: 1}
		proceed, started := make(chan struct{}), make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := c.Acquire(ctx, "key", func(context.Context) (*resource, error) { close(started); <-proceed; return old, nil })
			done <- err
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		newLease := acquire(t, c, "key", fresh)
		close(proceed)
		synctest.Wait()
		if old.closed.Load() != 1 {
			t.Fatal("abandoned result leaked")
		}
		hit := acquire(t, c, "key", old)
		if hit.Value() != fresh {
			t.Fatal("late canceled fill replaced fresh result")
		}
		_ = hit.Close()
		_ = newLease.Close()
		_ = c.Close()
	})
}

func TestCloseUnblocksWaitersEvenWhenLoaderIgnoresCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testCache(t, 2, 10)
		r := &resource{weight: 1}
		started, proceed := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := c.Acquire(t.Context(), "key", func(context.Context) (*resource, error) { close(started); <-proceed; return r, nil })
			done <- err
		}()
		<-started
		_ = c.Close()
		if err := <-done; !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
		close(proceed)
		synctest.Wait()
		if r.closed.Load() != 1 {
			t.Fatal("shutdown leaked late load result")
		}
	})
}

func TestFailuresAndTimeoutsAreNotCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testCache(t, 2, 10)
		failure := errors.New("export failed")
		if _, err := c.Acquire(t.Context(), "key", func(context.Context) (*resource, error) { return nil, failure }); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if _, err := c.Acquire(t.Context(), "key", func(ctx context.Context) (*resource, error) { <-ctx.Done(); return nil, ctx.Err() }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		l := acquire(t, c, "key", &resource{weight: 1})
		_ = l.Close()
		_ = c.Close()
	})
}
