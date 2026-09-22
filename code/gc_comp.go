package jr_cache

import (
	"errors"
	"math"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// Errors related to the GC manager.
var (
	ErrInvalidInterval = errors.New("interval must be greater than 0")
	ErrNilCollector    = errors.New("collect function must not be nil")
	ErrAlreadyRunning  = errors.New("gc manager is already running")
)

// structs for the GC manager

// GCManager periodically sweeps an object on a background goroutine. It only
// decides *when* to collect; the collect function passed to the constructor
// decides *what* that means, so the manager works for anything that can be
// swept, not only the caches in this package.
//
// On every tick of Interval the manager collects if any enabled trigger
// fires:
//
//	MemoryThreshold  the heap holds at least this many bytes
//	TimeThreshold    at least this long has passed since the last collection
//	DateThreshold    this instant has passed (fires once; re-arm with
//	                 SetDateThreshold)
//
// A zero value disables that trigger. With no trigger enabled the manager
// collects on every tick. GCCollect collects immediately whatever the
// triggers say.
//
// All methods are safe to call from any goroutine, including from inside the
// collect function.
type GCManager[Object any] struct {
	object  Object
	collect func(Object) int

	dateThreshold time.Time
	// dateFired records that the one-shot date trigger has already gone off,
	// so it does not fire again. The date itself is kept so the manager
	// still counts as having a trigger configured.
	dateFired       bool
	timeThreshold   time.Duration
	memoryThreshold uint64
	interval        time.Duration

	// lastRun is when the last collection finished; collections and released
	// count what the manager has done so far.
	lastRun     time.Time
	collections uint64
	released    uint64

	// gcDisabled records that DisableGCCompletely turned the runtime
	// collector off, together with the settings to put back and whether the
	// sweeper was running at the time.
	gcDisabled     bool
	savedGCPercent int
	savedMemLimit  int64
	sweeperWasOn   bool

	// thread ticks the background loop; stop asks it to exit and done is
	// closed once it has.
	thread *time.Ticker
	stop   chan struct{}
	done   chan struct{}

	lock sync.RWMutex
	// gcLock serialises the whole disable/enable sequence. It is separate
	// from lock because that sequence calls Stop and Start, which take lock
	// themselves; it must never be acquired while holding lock.
	gcLock sync.Mutex
}

// GCConfig configures a GCManager. Only Interval is required, and only to
// Start the background loop.
type GCConfig struct {
	Interval        time.Duration
	TimeThreshold   time.Duration
	DateThreshold   time.Time
	MemoryThreshold uint64
}

// GCStats is a snapshot of what a manager has done.
type GCStats struct {
	Collections uint64
	Released    uint64
	LastRun     time.Time
	Running     bool
}

// interface for the GC manager

type GCManagerInterface[Object any] interface {
	GCCollect() int
	Start() error
	Stop()
	Running() bool
	Stats() GCStats
	SetDateThreshold(date time.Time)
	SetTimeThreshold(duration time.Duration)
	SetMemoryThreshold(memory uint64)
	SetInterval(interval time.Duration) error
	SetObject(obj Object)
	GetDateThreshold() time.Time
	GetTimeThreshold() time.Duration
	GetMemoryThreshold() uint64
	GetObject() Object
	GetInterval() time.Duration
	DisableGCCompletely() (gcPercent int, memLimit int64)
	EnableGCCompletely(gcPercent int, memLimit int64) error
	RestoreGC() error
	GCDisabled() bool
}

// Compile-time check that GCManager satisfies its interface.
var _ GCManagerInterface[int] = (*GCManager[int])(nil)

// constructors for the GC manager

// NewGCManager returns a manager for object. collect performs one collection
// and returns how many items it released; it must not be nil. The manager
// does not run until Start is called.
func NewGCManager[Object any](object Object, collect func(Object) int, config GCConfig) (*GCManager[Object], error) {
	if collect == nil {
		return nil, ErrNilCollector
	}
	if config.Interval < 0 {
		return nil, ErrInvalidInterval
	}
	return &GCManager[Object]{
		object:          object,
		collect:         collect,
		dateThreshold:   config.DateThreshold,
		timeThreshold:   config.TimeThreshold,
		memoryThreshold: config.MemoryThreshold,
		interval:        config.Interval,
		lastRun:         time.Now(),
	}, nil
}

// NewCacheGCManager returns a manager that sweeps the expired entries of a
// cache, the usual case. It is NewGCManager with DeleteExpired as the
// collect function.
func NewCacheGCManager[Key comparable, Value any](cache *CacheMap[Key, Value], config GCConfig) (*GCManager[*CacheMap[Key, Value]], error) {
	return NewGCManager(cache, func(c *CacheMap[Key, Value]) int {
		return c.DeleteExpired()
	}, config)
}

// NewShardedCacheGCManager is NewCacheGCManager for a sharded cache.
func NewShardedCacheGCManager[Key comparable, Value any](cache *ShardedCacheMap[Key, Value], config GCConfig) (*GCManager[*ShardedCacheMap[Key, Value]], error) {
	return NewGCManager(cache, func(c *ShardedCacheMap[Key, Value]) int {
		return c.DeleteExpired()
	}, config)
}

// private methods for the GC manager

// heap_in_use returns the bytes of allocated heap objects. Reading it stops
// the world briefly, so it is only called when a memory threshold is set.
func heap_in_use() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// should_collect reports whether any enabled trigger has fired, and whether
// the date trigger was the one that fired (it only fires once).
func (gc *GCManager[Object]) should_collect(now time.Time) (collect bool, dateFired bool) {
	gc.lock.RLock()
	date := gc.dateThreshold
	fired := gc.dateFired
	age := gc.timeThreshold
	memory := gc.memoryThreshold
	last := gc.lastRun
	gc.lock.RUnlock()

	enabled := false
	if !date.IsZero() {
		// A date that has already fired still counts as configured, so the
		// manager does not fall back to collecting on every tick.
		enabled = true
		if !fired && !now.Before(date) {
			return true, true
		}
	}
	if age > 0 {
		enabled = true
		if now.Sub(last) >= age {
			return true, false
		}
	}
	if memory > 0 {
		enabled = true
		if heap_in_use() >= memory {
			return true, false
		}
	}
	// With no trigger configured the manager collects on every tick.
	return !enabled, false
}

// run is the background loop. It owns ticker and exits when stop is closed.
func (gc *GCManager[Object]) run(ticker *time.Ticker, stop chan struct{}, done chan struct{}) {
	defer close(done)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			collect, dateFired := gc.should_collect(now)
			if !collect {
				continue
			}
			if dateFired {
				// One-shot: mark it so it does not fire on every later tick.
				gc.lock.Lock()
				gc.dateFired = true
				gc.lock.Unlock()
			}
			gc.GCCollect()
		}
	}
}

// public methods for the GC manager

// Start launches the background loop. It returns ErrInvalidInterval if no
// interval is set and ErrAlreadyRunning if the manager is already running.
func (gc *GCManager[Object]) Start() error {
	gc.lock.Lock()
	if gc.thread != nil {
		gc.lock.Unlock()
		return ErrAlreadyRunning
	}
	if gc.interval <= 0 {
		gc.lock.Unlock()
		return ErrInvalidInterval
	}
	ticker := time.NewTicker(gc.interval)
	stop := make(chan struct{})
	done := make(chan struct{})
	gc.thread, gc.stop, gc.done = ticker, stop, done
	gc.lock.Unlock()

	go gc.run(ticker, stop, done)
	return nil
}

// Stop ends the background loop and waits for it to exit. It is safe to call
// on a manager that is not running, and safe to call more than once. Calling
// it from inside the collect function would deadlock, so it does not wait in
// that case: see the comment below.
func (gc *GCManager[Object]) Stop() {
	gc.lock.Lock()
	stop, done := gc.stop, gc.done
	gc.thread, gc.stop, gc.done = nil, nil, nil
	gc.lock.Unlock()

	if stop == nil {
		return
	}
	close(stop)
	// The loop calls GCCollect without holding the lock, so waiting here is
	// safe from any goroutine except the collector itself. Stopping from
	// inside collect is a caller error; the select keeps it from hanging
	// forever if it happens anyway.
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

// Running reports whether the background loop is active.
func (gc *GCManager[Object]) Running() bool {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.thread != nil
}

// GCCollect runs one collection now and returns how many items it released.
// The collect function runs without the manager's lock held, so it may call
// back into the manager.
func (gc *GCManager[Object]) GCCollect() int {
	gc.lock.RLock()
	object, collect := gc.object, gc.collect
	gc.lock.RUnlock()

	if collect == nil {
		return 0
	}
	released := collect(object)
	if released < 0 {
		released = 0
	}

	gc.lock.Lock()
	gc.lastRun = time.Now()
	gc.collections++
	gc.released += uint64(released)
	gc.lock.Unlock()
	return released
}

// Stats returns a snapshot of the manager's counters.
func (gc *GCManager[Object]) Stats() GCStats {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return GCStats{
		Collections: gc.collections,
		Released:    gc.released,
		LastRun:     gc.lastRun,
		Running:     gc.thread != nil,
	}
}

// SetInterval changes how often the background loop ticks, resetting a
// running loop to the new period. An interval of 0 or less is rejected.
func (gc *GCManager[Object]) SetInterval(interval time.Duration) error {
	if interval <= 0 {
		return ErrInvalidInterval
	}
	gc.lock.Lock()
	defer gc.lock.Unlock()
	gc.interval = interval
	if gc.thread != nil {
		gc.thread.Reset(interval)
	}
	return nil
}

// SetDateThreshold arms a one-shot collection at date, re-arming it if it has
// already fired. A zero time disables it.
func (gc *GCManager[Object]) SetDateThreshold(date time.Time) {
	gc.lock.Lock()
	defer gc.lock.Unlock()
	gc.dateThreshold = date
	gc.dateFired = false
}

// DateThresholdFired reports whether the one-shot date trigger has gone off.
func (gc *GCManager[Object]) DateThresholdFired() bool {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return !gc.dateThreshold.IsZero() && gc.dateFired
}

// SetTimeThreshold collects once this long has passed since the last
// collection. Zero disables it.
func (gc *GCManager[Object]) SetTimeThreshold(duration time.Duration) {
	gc.lock.Lock()
	defer gc.lock.Unlock()
	gc.timeThreshold = duration
}

// SetMemoryThreshold collects once the heap holds at least memory bytes.
// Zero disables it.
func (gc *GCManager[Object]) SetMemoryThreshold(memory uint64) {
	gc.lock.Lock()
	defer gc.lock.Unlock()
	gc.memoryThreshold = memory
}

// SetObject swaps the object that is collected.
func (gc *GCManager[Object]) SetObject(obj Object) {
	gc.lock.Lock()
	defer gc.lock.Unlock()
	gc.object = obj
}

// SetCollector swaps the collect function. A nil function is ignored.
func (gc *GCManager[Object]) SetCollector(collect func(Object) int) {
	if collect == nil {
		return
	}
	gc.lock.Lock()
	defer gc.lock.Unlock()
	gc.collect = collect
}

func (gc *GCManager[Object]) GetDateThreshold() time.Time {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.dateThreshold
}

func (gc *GCManager[Object]) GetTimeThreshold() time.Duration {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.timeThreshold
}

func (gc *GCManager[Object]) GetMemoryThreshold() uint64 {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.memoryThreshold
}

func (gc *GCManager[Object]) GetInterval() time.Duration {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.interval
}

func (gc *GCManager[Object]) GetObject() Object {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.object
}

// DisableGCCompletely stops this manager's sweeper and turns the Go runtime
// collector off: SetGCPercent(-1) plus a memory limit of math.MaxInt64, since
// a memory limit still triggers collections on its own.
//
// It returns the previous GC percentage and memory limit so they can be put
// back with EnableGCCompletely; the manager also remembers them, so RestoreGC
// can do it without the caller holding on to anything.
//
// Two warnings. These are **process-global** runtime settings, not per
// manager: turning them off here turns them off for the whole program, so a
// library should not call this on a caller's behalf. And with the collector
// off the heap only grows — nothing is ever reclaimed — so use it for a
// bounded piece of work and turn it back on afterwards.
//
// Calling it twice is a no-op: the second call returns the same saved
// settings rather than saving "already disabled" over them.
func (gc *GCManager[Object]) DisableGCCompletely() (gcPercent int, memLimit int64) {
	gc.gcLock.Lock()
	defer gc.gcLock.Unlock()
	return gc.disable_locked()
}

// disable_locked does the work of DisableGCCompletely. The caller holds
// gcLock, which makes the "is it already disabled" check and the settings
// change one atomic step: without it two goroutines can both get past the
// check, and the second one saves the already-disabled -1 as the value to
// restore, leaving the collector off for good.
func (gc *GCManager[Object]) disable_locked() (gcPercent int, memLimit int64) {
	gc.lock.RLock()
	disabled := gc.gcDisabled
	savedPercent, savedLimit := gc.savedGCPercent, gc.savedMemLimit
	wasOn := gc.thread != nil
	gc.lock.RUnlock()

	if disabled {
		return savedPercent, savedLimit
	}

	// Stop takes lock itself, so it must be called without holding it.
	gc.Stop()

	gcPercent = debug.SetGCPercent(-1)
	memLimit = debug.SetMemoryLimit(math.MaxInt64)

	gc.lock.Lock()
	gc.gcDisabled = true
	gc.savedGCPercent, gc.savedMemLimit, gc.sweeperWasOn = gcPercent, memLimit, wasOn
	gc.lock.Unlock()
	return gcPercent, memLimit
}

// EnableGCCompletely puts the runtime collector back to the given settings —
// the pair returned by DisableGCCompletely — and restarts this manager's
// sweeper if it was running when the collector was turned off.
//
// It returns whatever Start returns, so a manager with no interval reports
// ErrInvalidInterval instead of silently staying stopped.
func (gc *GCManager[Object]) EnableGCCompletely(gcPercent int, memLimit int64) error {
	gc.gcLock.Lock()
	defer gc.gcLock.Unlock()
	return gc.enable_locked(gcPercent, memLimit)
}

// enable_locked does the work of EnableGCCompletely; the caller holds gcLock.
func (gc *GCManager[Object]) enable_locked(gcPercent int, memLimit int64) error {
	debug.SetGCPercent(gcPercent)
	debug.SetMemoryLimit(memLimit)

	gc.lock.Lock()
	gc.gcDisabled = false
	restart := gc.sweeperWasOn
	gc.sweeperWasOn = false
	gc.lock.Unlock()

	if !restart {
		return nil
	}
	// Start takes lock, so it is called after releasing it.
	if err := gc.Start(); err != nil && !errors.Is(err, ErrAlreadyRunning) {
		return err
	}
	return nil
}

// RestoreGC is EnableGCCompletely using the settings the manager saved when
// DisableGCCompletely was called. It does nothing if the collector is not
// currently disabled through this manager.
func (gc *GCManager[Object]) RestoreGC() error {
	gc.gcLock.Lock()
	defer gc.gcLock.Unlock()

	gc.lock.RLock()
	disabled := gc.gcDisabled
	gcPercent, memLimit := gc.savedGCPercent, gc.savedMemLimit
	gc.lock.RUnlock()

	if !disabled {
		return nil
	}
	return gc.enable_locked(gcPercent, memLimit)
}

// GCDisabled reports whether this manager turned the runtime collector off
// and has not put it back yet.
func (gc *GCManager[Object]) GCDisabled() bool {
	gc.lock.RLock()
	defer gc.lock.RUnlock()
	return gc.gcDisabled
}
