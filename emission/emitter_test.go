package emission

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A once listener is called by the next Emit and then taken off the list.
func TestOnceFiresOnce(t *testing.T) {
	e := NewEmitter()
	var calls int32
	e.Once("data", func() { atomic.AddInt32(&calls, 1) })

	e.Emit("data")
	e.Emit("data")
	e.Emit("data")

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("a once listener ran %d times, want 1", got)
	}
}

// The connection sequence waits for the message it wants by registering a
// handler that re-registers itself when the message turns out to be another one.
// That handler must end up registered once, not twice.
//
// This is the bug that crashed a gateway process. Once appended to a list that
// was only cleared after the listeners had run, so a re-arming listener added an
// entry to the list that was being consumed, and the next delivery ran the
// handler twice. Two goroutines writing the same map is a run time fatal error
// and recover does not catch it, so the whole process went down.
func TestOnceThatRearmsItselfEndsUpRegisteredOnce(t *testing.T) {
	e := NewEmitter()
	var calls int32

	// A handler in the shape the connection sequence uses: it is not interested
	// in this message, so it re-arms itself and returns.
	var arm func()
	arm = func() {
		atomic.AddInt32(&calls, 1)
		e.Once("data", arm)
	}
	e.Once("data", arm)

	// Ten deliveries. Each must run the handler exactly once: what the handler
	// does is put itself back, not double itself.
	for i := 0; i < 10; i++ {
		before := atomic.LoadInt32(&calls)
		e.Emit("data")
		if after := atomic.LoadInt32(&calls); after != before+1 {
			t.Fatalf("delivery %d ran the handler %d times, want 1", i, after-before)
		}
	}
}

// The crash was two deliveries seeing the same pending listener and both running
// it. Taking the list before running it is what stops that, so the same listener
// started from two goroutines fires once in total.
func TestOnceUnderConcurrentEmitsFiresOnce(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		e := NewEmitter()
		var calls int32
		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(2)

		e.Once("data", func() {
			atomic.AddInt32(&calls, 1)
		})

		for i := 0; i < 2; i++ {
			go func() {
				defer done.Done()
				start.Wait()
				e.Emit("data")
			}()
		}
		start.Done()
		done.Wait()

		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Fatalf("attempt %d: a once listener ran %d times across two concurrent emits, want 1", attempt, got)
		}
	}
}

// Distinct listeners on one event all run, in whatever order they happen to be
// scheduled. Both are called by one Emit.
func TestSeveralOncesOnOneEventAllRun(t *testing.T) {
	e := NewEmitter()
	var mu sync.Mutex
	seen := map[string]bool{}
	for _, name := range []string{"first", "second", "third"} {
		n := name
		e.Once("data", func() {
			mu.Lock()
			seen[n] = true
			mu.Unlock()
		})
	}

	e.Emit("data")

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Errorf("ran %d of 3 listeners: %v", len(seen), seen)
	}
}

// Emit waits for its listeners, which is what lets a caller rely on the work
// being done when Emit returns.
func TestEmitWaitsForListeners(t *testing.T) {
	e := NewEmitter()
	var done int32
	e.On("data", func() {
		time.Sleep(20 * time.Millisecond)
		atomic.StoreInt32(&done, 1)
	})

	e.Emit("data")

	if atomic.LoadInt32(&done) != 1 {
		t.Error("Emit returned before its listener finished")
	}
}

// Listeners registered with On stay registered, and every Emit runs them.
func TestOnFiresEveryTime(t *testing.T) {
	e := NewEmitter()
	var calls int32
	e.On("data", func() { atomic.AddInt32(&calls, 1) })

	e.Emit("data")
	e.Emit("data")

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("a persistent listener ran %d times, want 2", got)
	}
}

// An event with nothing registered is not an error, and emitting one must not
// block or panic.
func TestEmitWithNoListeners(t *testing.T) {
	e := NewEmitter()
	e.Emit("nothing-registered")
	e.Once("registered", func() {})
	e.Emit("other-event")
	e.Emit("registered")
}
