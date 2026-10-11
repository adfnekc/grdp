// Package emission provides an event emitter.
// copy form https://raw.githubusercontent.com/chuckpreslar/emission/master/emitter.go
// fix issue with nest once
//
// Listeners are called directly, in the order they were registered, on the
// goroutine that emits. They used to be called through reflect.Value.Call, one
// goroutine each, with the emit waiting for all of them. Both halves of that cost
// a fixed amount per delivery, and delivery happens on every received PDU at every
// layer of the stack, which made it worth removing rather than tolerating.
//
// The reflection is gone for every shape that occurs on a hot path: a listener is
// bound to its arguments once, when it is registered, and calling it afterwards is
// an ordinary call. A shape that is not one of those falls back to reflection, and
// a shape that is not a function at all is still an error.
//
// The goroutines are gone entirely, which also removes the concurrency the
// package used to have: two listeners for one event cannot run at once any more,
// and a listener that re-registers itself is safe for a stronger reason than
// before. An emit still does not return until its listeners have returned.

package emission

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
)

// Default number of maximum listeners for an event.
const DefaultMaxListeners = 10

// Error presented when an invalid argument is provided as a listener function
var ErrNoneFunction = errors.New("Kind of Value for listener is not Func.")

// RecoveryListener ...
type RecoveryListener func(interface{}, interface{}, error)

// listener is a registered handler, with the argument conversion already decided.
//
// fn is the value as it was registered, which is what removal compares by and what
// the recovery listener is given. call is the same handler with its arguments
// bound, so that delivering an event is a call rather than a reflection.
type listener struct {
	fn   interface{}
	call func(...interface{})
}

// Emitter ...
type Emitter struct {
	// Mutex to prevent race conditions within the Emitter.
	*sync.Mutex
	// Map of event to a slice of listeners.
	events map[interface{}][]listener
	// Optional RecoveryListener to call when a panic occurs.
	recoverer RecoveryListener
	// Maximum listeners for debugging potential memory leaks.
	maxListeners int

	// Map of event to a slice of once listeners.
	onces map[interface{}][]listener
}

// bind returns the handler with its arguments bound, so that delivery does not go
// through reflection.
//
// Each case is a shape that occurs in this library on a per packet path, which is
// why they are written out rather than being handled generically: the generic
// answer is reflect.Value.Call, and that is the cost this exists to remove. The
// arguments are checked as they are extracted, and a mismatch panics with the
// names of what was given, which is what the reflective call used to do in its own
// words.
func bind(fn interface{}) (func(...interface{}), bool) {
	switch f := fn.(type) {
	case func():
		return func(...interface{}) { f() }, true

	case func([]byte):
		return func(args ...interface{}) {
			if len(args) != 1 {
				panic(mismatch(fn, args))
			}
			b, ok := args[0].([]byte)
			if !ok {
				panic(mismatch(fn, args))
			}
			f(b)
		}, true

	case func(error):
		return func(args ...interface{}) {
			if len(args) != 1 {
				panic(mismatch(fn, args))
			}
			e, ok := args[0].(error)
			if !ok {
				panic(mismatch(fn, args))
			}
			f(e)
		}, true

	case func(interface{}):
		return func(args ...interface{}) { f(one(args, fn)) }, true

	case func(string, []byte):
		return func(args ...interface{}) {
			if len(args) != 2 {
				panic(mismatch(fn, args))
			}
			s, ok := args[0].(string)
			if !ok {
				panic(mismatch(fn, args))
			}
			b, ok := args[1].([]byte)
			if !ok {
				panic(mismatch(fn, args))
			}
			f(s, b)
		}, true

	case func(string):
		return func(args ...interface{}) {
			s, ok := args[0].(string)
			if len(args) != 1 || !ok {
				panic(mismatch(fn, args))
			}
			f(s)
		}, true

	case func(int):
		return func(args ...interface{}) {
			v, ok := toInt(args, 1)
			if !ok {
				panic(mismatch(fn, args))
			}
			f(v[0])
		}, true

	case func(int, int):
		return func(args ...interface{}) {
			v, ok := toInt(args, 2)
			if !ok {
				panic(mismatch(fn, args))
			}
			f(v[0], v[1])
		}, true
	}
	return nil, false
}

func one(args []interface{}, fn interface{}) interface{} {
	if len(args) != 1 {
		panic(mismatch(fn, args))
	}
	return args[0]
}

func toInt(args []interface{}, n int) ([]int, bool) {
	if len(args) != n {
		return nil, false
	}
	out := make([]int, n)
	for i := range args {
		v, ok := args[i].(int)
		if !ok {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// reflectListener is the fallback for a shape that is not in the table, which is
// every event that fires once a session rather than once a packet.
func reflectListener(fn interface{}) func(...interface{}) {
	v := reflect.ValueOf(fn)
	t := v.Type()
	return func(args ...interface{}) {
		values := make([]reflect.Value, 0, len(args))
		for i := range args {
			if args[i] == nil {
				values = append(values, reflect.New(t.In(i)).Elem())
				continue
			}
			values = append(values, reflect.ValueOf(args[i]))
		}
		v.Call(values)
	}
}

func mismatch(fn interface{}, args []interface{}) error {
	return fmt.Errorf("emission: listener %T cannot take %d arguments of type %s",
		fn, len(args), typesOf(args))
}

func typesOf(args []interface{}) string {
	if len(args) == 0 {
		return "nothing"
	}
	s := fmt.Sprintf("%T", args[0])
	for _, a := range args[1:] {
		s += ", " + fmt.Sprintf("%T", a)
	}
	return s
}

// newListener validates a listener and binds it. A value that is not a function is
// reported through the recovery listener if there is one, and panics otherwise,
// which is what this package has always done.
func (emitter *Emitter) newListener(event, fn interface{}) (listener, bool) {
	if reflect.Func != reflect.ValueOf(fn).Kind() {
		if nil == emitter.recoverer {
			panic(ErrNoneFunction)
		}
		emitter.recoverer(event, fn, ErrNoneFunction)
		return listener{}, false
	}
	call, ok := bind(fn)
	if !ok {
		call = reflectListener(fn)
	}
	return listener{fn: fn, call: call}, true
}

// AddListener appends the listener argument to the event arguments slice
// in the Emitter's events map. If the number of listeners for an event
// is greater than the Emitter's maximum listeners then a warning is printed.
// If the value passed as the listener is not a function then AddListener
// panics. If a RecoveryListener has been set then it is called
// recovering from the panic.
func (emitter *Emitter) AddListener(event, listener interface{}) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	l, ok := emitter.newListener(event, listener)
	if !ok {
		return emitter
	}

	if emitter.maxListeners != -1 && emitter.maxListeners < len(emitter.events[event])+1 {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` has exceeded the maximum "+
			"number of listeners of %d.\n", event, emitter.maxListeners)
	}

	emitter.events[event] = append(emitter.events[event], l)

	return emitter
}

// On is an alias for AddListener.
func (emitter *Emitter) On(event, listener interface{}) *Emitter {
	return emitter.AddListener(event, listener)
}

// RemoveListener removes the listener argument from the event arguments slice
// in the Emitter's events map. If the value passed as the listener is not a
// function then RemoveListener panics. If a RecoveryListener has been set
// then it is called after recovering from the panic.
//
// Listeners are identified by the code pointer of the function that was
// registered, which is what this has always done. Two closures over the same code,
// and two method values of the same method on different receivers, therefore
// compare equal; removal removes the first match.
func (emitter *Emitter) RemoveListener(event, listener interface{}) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	v := reflect.ValueOf(listener)

	if reflect.Func != v.Kind() {
		if nil == emitter.recoverer {
			panic(ErrNoneFunction)
		}
		emitter.recoverer(event, listener, ErrNoneFunction)
		return emitter
	}

	target := v.Pointer()
	emitter.events[event] = removeByCode(emitter.events[event], target)
	emitter.onces[event] = removeByCode(emitter.onces[event], target)

	return emitter
}

func removeByCode(listeners []listener, target uintptr) []listener {
	kept := listeners[:0]
	for _, l := range listeners {
		if reflect.ValueOf(l.fn).Pointer() != target {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// Off is an alias for RemoveListener.
func (emitter *Emitter) Off(event, listener interface{}) *Emitter {
	return emitter.RemoveListener(event, listener)
}

// Once registers a listener that is called at most once, by the next Emit of
// that event, and is taken off the list before it runs.
//
// It appends rather than replaces, so registering the same function twice leaves
// two entries, and both are called by the next Emit. Callers that re-arm
// themselves as they run do not need to worry about that, because the list is
// cleared first: their append lands after the clear, so they end up registered
// once. Callers registering two different listeners for one event get both, in
// the order they were registered.
func (emitter *Emitter) Once(event, listener interface{}) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	l, ok := emitter.newListener(event, listener)
	if !ok {
		return emitter
	}

	if emitter.maxListeners != -1 && emitter.maxListeners < len(emitter.onces[event])+1 {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` has exceeded the maximum "+
			"number of listeners of %d.\n", event, emitter.maxListeners)
	}

	emitter.onces[event] = append(emitter.onces[event], l)
	return emitter
}

// Emit calls every listener registered for the event, in the order they were
// registered, and returns when they have all returned.
//
// The listeners run on the calling goroutine. Two concurrent Emits therefore still
// run concurrently with each other, but the listeners of one Emit do not run
// concurrently with each other, which is a change from what this package used to
// do and the reason a handler can no longer run twice at once. A listener
// registered twice can still run twice, once per registration and one after the
// other.
//
// A listener whose arguments do not match the event panics, and if a
// RecoveryListener has been set it is called instead and the remaining listeners
// still run; without one the panic propagates.
func (emitter *Emitter) Emit(event interface{}, arguments ...interface{}) *Emitter {
	var (
		listeners []listener
		ok        bool
	)

	// Lock the mutex when reading from the Emitter's
	// events map.
	emitter.Lock()

	if listeners, ok = emitter.events[event]; !ok {
		// If the Emitter does not include the event in its
		// event map, it has no listeners to Call yet.
		emitter.Unlock()
		goto ONCES
	}

	// Unlock the mutex immediately following the read
	// instead of deferring so that listeners registered
	// with Once can aquire the mutex for removal.
	emitter.Unlock()
	emitter.callListeners(listeners, event, arguments...)

ONCES:
	// Execute the once listeners. They are taken off the list BEFORE they run,
	// and not after.
	//
	// The difference matters because of what the callers of Once do. The
	// connection sequence waits for the next message by registering a handler
	// that re-registers itself when the message turns out not to be the one it
	// wants, so a listener running under Once is expected to append to this map.
	// Consuming after the run meant the append landed inside the list being
	// consumed, and two deliveries arriving close together could both see the
	// pending listener and both run it. For a handler that writes to a map that
	// is a fatal error rather than a panic, so it takes the process down.
	//
	// Taking the list first makes that impossible: a listener that re-arms adds
	// to an empty list and ends up registered exactly once, and a second
	// delivery finds nothing pending rather than firing the same handler again.
	emitter.Lock()
	if listeners, ok = emitter.onces[event]; !ok {
		emitter.Unlock()
		return emitter
	}
	delete(emitter.onces, event)
	emitter.Unlock()
	emitter.callListeners(listeners, event, arguments...)
	return emitter
}

func (emitter *Emitter) callListeners(listeners []listener, event interface{}, arguments ...interface{}) {
	// Read the recovery listener once, under the lock, so that setting it while an
	// emit is in progress is not a data race.
	emitter.Lock()
	recoverer := emitter.recoverer
	emitter.Unlock()

	for _, l := range listeners {
		if nil == recoverer {
			l.call(arguments...)
			continue
		}
		// One frame per listener, so that a listener that panics does not take
		// the remaining listeners of this event with it.
		func() {
			defer func() {
				if r := recover(); nil != r {
					recoverer(event, l.fn, fmt.Errorf("%v", r))
				}
			}()
			l.call(arguments...)
		}()
	}
}

// RecoverWith sets the listener to call when a panic occurs, recovering from
// panics and attempting to keep the application from crashing.
func (emitter *Emitter) RecoverWith(listener RecoveryListener) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	emitter.recoverer = listener
	return emitter
}

// SetMaxListeners sets the maximum number of listeners per
// event for the Emitter. If -1 is passed as the maximum,
// all events may have unlimited listeners. By default, each
// event can have a maximum number of 10 listeners which is
// useful for finding memory leaks.
func (emitter *Emitter) SetMaxListeners(max int) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	emitter.maxListeners = max
	return emitter
}

// GetListenerCount gets count of listeners for a given event.
func (emitter *Emitter) GetListenerCount(event interface{}) (count int) {
	emitter.Lock()
	if listeners, ok := emitter.events[event]; ok {
		count = len(listeners)
	}
	emitter.Unlock()
	return
}

// NewEmitter returns a new Emitter object, defaulting the
// number of maximum listeners per event to the DefaultMaxListeners
// constant and initializing its events map.
func NewEmitter() (emitter *Emitter) {
	emitter = new(Emitter)
	emitter.Mutex = new(sync.Mutex)
	emitter.events = make(map[interface{}][]listener)
	emitter.maxListeners = DefaultMaxListeners
	emitter.onces = make(map[interface{}][]listener)
	return
}
