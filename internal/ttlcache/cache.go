// Package ttlcache provides bounded, process-local TTL caches with leased values.
// Values are immutable after publication. Expiry and eviction stop new leases;
// disposal waits for existing leases to finish. There is no persistent storage.
package ttlcache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrClosed = errors.New("TTL cache is closed")

type Options[V any] struct {
	TTL         time.Duration
	LoadTimeout time.Duration
	MaxEntries  int
	MaxWeight   int64
	Weight      func(V) int64
	Dispose     func(V) error
	// OnError reports disposal failures from timer expiry or abandoned loads.
	OnError func(error)
}

type Cache[K comparable, V any] struct {
	mu      sync.Mutex
	options Options[V]
	entries map[K]*entry[K, V]
	loads   map[K]*flight[K, V]
	lru     list.List
	weight  int64
	closed  bool
	done    chan struct{}
}

type entry[K comparable, V any] struct {
	key     K
	value   V
	weight  int64
	refs    int
	expires time.Time
	timer   *time.Timer
	element *list.Element
	retired bool
}

type flight[K comparable, V any] struct {
	ctx       context.Context
	done      chan struct{}
	cancel    context.CancelFunc
	waiters   int
	completed bool
	entry     *entry[K, V]
	err       error
}

// Lease owns one reference. Its value must not be mutated or disposed directly.
// Call Close on completion or cancellation; Close is safe to call repeatedly.
type Lease[V any] struct {
	value   V
	once    sync.Once
	release func() error
	err     error
}

func (l *Lease[V]) Value() V { return l.value }
func (l *Lease[V]) Close() error {
	l.once.Do(func() { l.err = l.release() })
	return l.err
}

func New[K comparable, V any](options Options[V]) (*Cache[K, V], error) {
	if options.TTL <= 0 || options.LoadTimeout <= 0 || options.MaxEntries <= 0 || options.MaxWeight <= 0 {
		return nil, fmt.Errorf("TTL, load timeout, entry limit, and weight limit must be positive")
	}
	if options.Weight == nil || options.Dispose == nil {
		return nil, fmt.Errorf("weight and disposal functions are required")
	}
	return &Cache[K, V]{options: options, entries: make(map[K]*entry[K, V]), loads: make(map[K]*flight[K, V]), done: make(chan struct{})}, nil
}

// Acquire coalesces concurrent misses. The loader owns its resources until a
// successful return transfers them to the cache. A failed loader must clean up.
// Loaders use a bounded cache-owned context, independent of any single waiter.
// When all waiters leave, the load is canceled and its eventual result disposed.
func (c *Cache[K, V]) Acquire(ctx context.Context, key K, load func(context.Context) (V, error)) (*Lease[V], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	var dispose []*entry[K, V]
	if e := c.entries[key]; e != nil {
		if time.Now().Before(e.expires) {
			e.refs++
			c.lru.MoveToFront(e.element)
			c.mu.Unlock()
			return c.lease(e), nil
		}
		dispose = c.retire(e, dispose)
	}
	f := c.loads[key]
	if f == nil {
		loadCtx, cancel := context.WithTimeout(context.Background(), c.options.LoadTimeout)
		f = &flight[K, V]{ctx: loadCtx, done: make(chan struct{}), cancel: cancel}
		c.loads[key] = f
		go c.fill(loadCtx, key, f, load)
	}
	f.waiters++
	c.mu.Unlock()
	c.report(c.dispose(dispose))
	select {
	case <-ctx.Done():
	case <-f.done:
	case <-f.ctx.Done():
	case <-c.done:
	}
	return c.finish(ctx, key, f)
}

func (c *Cache[K, V]) finish(ctx context.Context, key K, f *flight[K, V]) (*Lease[V], error) {
	c.mu.Lock()
	f.waiters--
	err := ctx.Err()
	if err == nil && c.closed {
		err = ErrClosed
	}
	if !f.completed {
		if err == nil {
			err = f.ctx.Err()
		}
		if f.waiters == 0 {
			if c.loads[key] == f {
				delete(c.loads, key)
			}
			f.cancel()
		}
		c.mu.Unlock()
		return nil, err
	}
	if err == nil {
		err = f.err
	}
	e := f.entry
	if err == nil {
		c.mu.Unlock()
		return c.lease(e), nil
	}
	var dispose []*entry[K, V]
	if e != nil {
		dispose = c.releaseLocked(e)
	}
	c.mu.Unlock()
	c.report(c.dispose(dispose))
	return nil, err
}

func (c *Cache[K, V]) fill(ctx context.Context, key K, f *flight[K, V], load func(context.Context) (V, error)) {
	defer f.cancel()
	value, err := load(ctx)
	owned := err == nil
	var weight int64
	if owned {
		weight = c.options.Weight(value)
		if weight < 0 {
			err = fmt.Errorf("cache value has negative weight")
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	c.mu.Lock()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if c.closed {
		err = ErrClosed
	}
	if f.waiters == 0 && err == nil {
		err = context.Canceled
	}
	var dispose []*entry[K, V]
	if err == nil {
		e := &entry[K, V]{key: key, value: value, weight: weight, refs: f.waiters, expires: time.Now().Add(c.options.TTL)}
		f.entry = e
		if weight > c.options.MaxWeight {
			// Oversized values are shared with current waiters but not retained.
			e.retired = true
		} else {
			for len(c.entries) >= c.options.MaxEntries || c.weight > c.options.MaxWeight-weight {
				dispose = c.retire(c.lru.Back().Value.(*entry[K, V]), dispose)
			}
			e.element = c.lru.PushFront(e)
			c.entries[key] = e
			c.weight += weight
			e.timer = time.AfterFunc(c.options.TTL, func() { c.expire(e) })
		}
		owned = false
	}
	f.err, f.completed = err, true
	if c.loads[key] == f {
		delete(c.loads, key)
	}
	close(f.done)
	c.mu.Unlock()
	if owned {
		c.report(c.options.Dispose(value))
	}
	c.report(c.dispose(dispose))
}

func (c *Cache[K, V]) lease(e *entry[K, V]) *Lease[V] {
	return &Lease[V]{value: e.value, release: func() error {
		c.mu.Lock()
		dispose := c.releaseLocked(e)
		c.mu.Unlock()
		return c.dispose(dispose)
	}}
}

func (c *Cache[K, V]) releaseLocked(e *entry[K, V]) []*entry[K, V] {
	e.refs--
	if e.retired && e.refs == 0 {
		return []*entry[K, V]{e}
	}
	return nil
}

// retire runs under mu, but disposal always runs outside it.
func (c *Cache[K, V]) retire(e *entry[K, V], dispose []*entry[K, V]) []*entry[K, V] {
	delete(c.entries, e.key)
	c.lru.Remove(e.element)
	c.weight -= e.weight
	e.retired = true
	if e.timer != nil {
		e.timer.Stop()
	}
	if e.refs == 0 {
		dispose = append(dispose, e)
	}
	return dispose
}

func (c *Cache[K, V]) expire(e *entry[K, V]) {
	c.mu.Lock()
	var dispose []*entry[K, V]
	if c.entries[e.key] == e {
		dispose = c.retire(e, dispose)
	}
	c.mu.Unlock()
	c.report(c.dispose(dispose))
}

// Close rejects new leases, cancels loads, and retires retained entries. Active
// leases remain usable until released. Loaders that ignore cancellation are
// disposed when they return; Close does not wait for them or active leases.
func (c *Cache[K, V]) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	var dispose []*entry[K, V]
	for _, e := range c.entries {
		dispose = c.retire(e, dispose)
	}
	for _, f := range c.loads {
		f.cancel()
	}
	c.mu.Unlock()
	return c.dispose(dispose)
}

func (c *Cache[K, V]) dispose(entries []*entry[K, V]) error {
	var failures []error
	for _, e := range entries {
		failures = append(failures, c.options.Dispose(e.value))
	}
	return errors.Join(failures...)
}

func (c *Cache[K, V]) report(err error) {
	if err != nil && c.options.OnError != nil {
		c.options.OnError(err)
	}
}
