package emission

import "testing"

// These measure the delivery path as the protocol layers use it: one emit per
// received PDU, at every layer, with the payload as the only argument. That is the
// shape the file transfer profile named, so it is the one worth a number.

func BenchmarkEmitOneByteSliceListener(b *testing.B) {
	e := NewEmitter()
	payload := make([]byte, 1<<10)
	var got int
	e.On("data", func(s []byte) { got = len(s) })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Emit("data", payload)
	}
	if got != len(payload) {
		b.Fatalf("listener saw %d bytes, want %d", got, len(payload))
	}
}

func BenchmarkEmitNoArguments(b *testing.B) {
	e := NewEmitter()
	var n int
	e.On("close", func() { n++ })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Emit("close")
	}
	if n != b.N {
		b.Fatalf("listener ran %d times, want %d", n, b.N)
	}
}

// Three listeners on one event, which is what a layer with several interests looks
// like. Each counter is its own variable because the emitter used to run the
// listeners of one event concurrently, and a shared counter would be a race that
// the benchmark could not measure.
func BenchmarkEmitThreeListeners(b *testing.B) {
	e := NewEmitter()
	payload := make([]byte, 256)
	var a, c, d int
	e.On("data", func(s []byte) { a++ })
	e.On("data", func(s []byte) { c++ })
	e.On("data", func(s []byte) { d++ })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Emit("data", payload)
	}
	if a != b.N || c != b.N || d != b.N {
		b.Fatalf("listeners ran %d, %d, %d times, want %d each", a, c, d, b.N)
	}
}

// A listener registered for an event that carries an interface{} is already
// untyped at its own boundary, so it is the shape reflection is least needed for.
func BenchmarkEmitInterfaceListener(b *testing.B) {
	e := NewEmitter()
	var got interface{}
	e.On("pointer", func(d interface{}) { got = d })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Emit("pointer", 42)
	}
	if got == nil {
		b.Fatal("listener did not run")
	}
}

// The shapes the receive path actually registers, each of which must be bound so
// that delivering an event is an ordinary call. If one of these ever falls out of
// the binding table, delivery for that event silently goes back to reflection and
// the cost comes back per packet.
func TestReceivePathShapesAreBound(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   interface{}
	}{
		{"func([]byte) for data", func(b []byte) {}},
		{"func(error) for error", func(e error) {}},
		{"func() for close and ready", func() {}},
		{"func(string, []byte) for channel", func(s string, b []byte) {}},
		{"func(interface{}) for orders and pointer", func(d interface{}) {}},
	} {
		if _, ok := bind(tc.fn); !ok {
			t.Errorf("%s is not bound, so delivery goes through reflection", tc.name)
		}
	}
}

// A shape that is not in the table falls back to reflection. That is deliberate:
// the table covers what is delivered per packet, and the rest fire once a session
// or once a frame.
func TestUnboundShapesFallBackToReflection(t *testing.T) {
	type thing struct{ x int }
	if _, ok := bind(func(v *thing) {}); ok {
		t.Error("a shape that is not in the table was reported as bound")
	}
	if _, ok := bind(func(a int, b string, c float64) {}); ok {
		t.Error("a three argument shape was reported as bound")
	}
}

// What an unbound listener costs, for comparison with the bound ones above. This is
// the price of the fallback, paid by events that are not on a per packet path.
func BenchmarkEmitUnboundListener(b *testing.B) {
	e := NewEmitter()
	type thing struct{ x int }
	var got *thing
	e.On("bitmap", func(v *thing) { got = v })
	v := &thing{x: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Emit("bitmap", v)
	}
	if got == nil {
		b.Fatal("listener did not run")
	}
}
