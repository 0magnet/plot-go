package plot

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// mustBlock and mustSimple build a processor the test author knows is valid.
func mustBlock(t *testing.T, n int) *Block {
	t.Helper()
	b, err := NewBlock(n)
	if err != nil {
		t.Fatalf("NewBlock(%d): %v", n, err)
	}
	return b
}

func mustSimple(t *testing.T, n int) *Simple {
	t.Helper()
	s, err := NewSimple(n)
	if err != nil {
		t.Fatalf("NewSimple(%d): %v", n, err)
	}
	return s
}

func mustRate(t *testing.T, iv float64) *Rate {
	t.Helper()
	r, err := NewRate(iv)
	if err != nil {
		t.Fatalf("NewRate(%v): %v", iv, err)
	}
	return r
}

func closeAll(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d values %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if !near(got[i], want[i]) {
			t.Errorf("value %d = %v, want %v (got %v)", i, got[i], want[i], got)
			return
		}
	}
}

func seq(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i)
	}
	return out
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// ── Block ────────────────────────────────────────────────────────────────────

func TestBlockAverages(t *testing.T) {
	b, err := NewBlock(3)
	if err != nil {
		t.Fatal(err)
	}
	// 0..9. Blocks of three: (0+1+2)/3, (3+4+5)/3, (6+7+8)/3. The last block
	// needs a sample beyond it before it is emitted, and 9 is that sample.
	got, consumed := b.Process(nil, seq(10))
	closeAll(t, got, []float64{1, 4, 7})
	if consumed != 9 {
		t.Errorf("consumed %d, want 9", consumed)
	}
}

// A block is emitted only once something follows it, which is what keeps a
// stream from averaging over a window that is still filling.
func TestBlockWaitsForASampleBeyondTheBlock(t *testing.T) {
	b := mustBlock(t, 4)
	got, consumed := b.Process(nil, seq(4))
	if len(got) != 0 || consumed != 0 {
		t.Errorf("exactly one block gave %v consuming %d; it should wait", got, consumed)
	}
	got, consumed = b.Process(nil, seq(5))
	closeAll(t, got, []float64{1.5}) // (0+1+2+3)/4
	if consumed != 4 {
		t.Errorf("consumed %d, want 4", consumed)
	}
}

func TestBlockRejectsBadSizes(t *testing.T) {
	for _, n := range []int{0, -1, BufferSize, BufferSize + 1} {
		if _, err := NewBlock(n); err == nil {
			t.Errorf("block size %d was accepted", n)
		}
	}
}

// ── Simple ───────────────────────────────────────────────────────────────────

// The point of a moving average is that it does not move a flat signal. The C
// original divides the warm-up sum by one less than it holds and so reports
// 15, 13.33, 12.5 for a constant 10; this is the divergence that fixes.
func TestSimpleKeepsAFlatSignalFlat(t *testing.T) {
	s, err := NewSimple(5)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Process(nil, flat(20, 10))
	if len(got) == 0 {
		t.Fatal("no output")
	}
	for i, v := range got {
		if !near(v, 10) {
			t.Errorf("value %d of a constant 10 series is %v, want 10 (full: %v)", i, v, got)
			break
		}
	}
}

// Output starts once the window is more than half full, which is the schedule
// the original uses.
func TestSimpleStartsAtHalfAWindow(t *testing.T) {
	s := mustSimple(t, 5)
	// Six samples: warm-up emits at the 3rd, 4th and 5th, then the sliding
	// pass has one full window available.
	got, _ := s.Process(nil, flat(6, 1))
	if len(got) < 3 {
		t.Errorf("got %d values from 6 samples, want at least the 3 warm-up ones: %v", len(got), got)
	}
}

func TestSimpleSmoothsASpike(t *testing.T) {
	s := mustSimple(t, 5)
	in := flat(21, 0)
	in[10] = 100
	got, _ := s.Process(nil, in)

	peak := 0.0
	for _, v := range got {
		if v > peak {
			peak = v
		}
	}
	if peak >= 100 {
		t.Errorf("the spike came through at %v; a 5-wide average should flatten it to about 20", peak)
	}
	if peak <= 0 {
		t.Errorf("the spike vanished entirely (peak %v)", peak)
	}
}

func TestSimpleRequiresAnOddWindow(t *testing.T) {
	for _, n := range []int{2, 4, 100} {
		if _, err := NewSimple(n); err == nil {
			t.Errorf("even window %d was accepted", n)
		}
	}
	for _, n := range []int{1, 3, 5, 127} {
		if _, err := NewSimple(n); err != nil {
			t.Errorf("odd window %d was rejected: %v", n, err)
		}
	}
}

func TestSimpleRejectsBadWindows(t *testing.T) {
	for _, n := range []int{0, -1, BufferSize, BufferSize + 1} {
		if _, err := NewSimple(n); err == nil {
			t.Errorf("window %d was accepted", n)
		}
	}
}

// ── Cumulative ───────────────────────────────────────────────────────────────

func TestCumulativeAverages(t *testing.T) {
	c := NewCumulative()
	got, consumed := c.Process(nil, []float64{1, 2, 3, 4})
	closeAll(t, got, []float64{1, 1.5, 2, 2.5})
	if consumed != 4 {
		t.Errorf("consumed %d, want 4", consumed)
	}
}

// It never forgets, so it settles rather than tracks: a step change moves it
// less and less as the history behind it grows.
func TestCumulativeSettles(t *testing.T) {
	c := NewCumulative()
	got, _ := c.Process(nil, flat(100, 5))
	if !near(got[len(got)-1], 5) {
		t.Errorf("a constant series settled at %v, want 5", got[len(got)-1])
	}

	step, _ := c.Process(nil, flat(1, 105))
	moved := step[0] - 5
	if moved <= 0 || moved >= 1 {
		t.Errorf("a +100 step after 100 samples moved the average by %v, want under 1", moved)
	}
}

// ── Rate ─────────────────────────────────────────────────────────────────────

// This is the one that makes a counter readable: a monotonic ramp becomes the
// rate it is climbing at.
func TestRateOfAConstantClimbIsConstant(t *testing.T) {
	r, err := NewRate(1)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]float64, 10)
	for i := range in {
		in[i] = float64(i * 7) // climbing by 7 every sample
	}
	got, consumed := r.Process(nil, in)
	if consumed != 10 {
		t.Errorf("consumed %d, want 10", consumed)
	}
	// The first has nothing to compare against.
	closeAll(t, got, []float64{0, 7, 7, 7, 7, 7, 7, 7, 7, 7})
}

func TestRateDividesByTheInterval(t *testing.T) {
	r := mustRate(t, 5)
	got, _ := r.Process(nil, []float64{0, 50, 100})
	closeAll(t, got, []float64{0, 10, 10})
}

func TestRateOfAFlatSignalIsZero(t *testing.T) {
	r := mustRate(t, 1)
	got, _ := r.Process(nil, flat(10, 42))
	for i, v := range got {
		if !near(v, 0) {
			t.Errorf("value %d = %v, want 0 — a constant value is not changing", i, v)
			break
		}
	}
}

func TestRateGoesNegativeWhenTheValueFalls(t *testing.T) {
	r := mustRate(t, 1)
	got, _ := r.Process(nil, []float64{100, 90, 70})
	closeAll(t, got, []float64{0, -10, -20})
}

func TestRateRejectsAZeroInterval(t *testing.T) {
	if _, err := NewRate(0); err == nil {
		t.Error("a zero interval was accepted; it would divide by zero")
	}
}

func TestRateOnNoInput(t *testing.T) {
	r := mustRate(t, 1)
	got, consumed := r.Process(nil, nil)
	if len(got) != 0 || consumed != 0 {
		t.Errorf("empty input gave %v consuming %d", got, consumed)
	}
}

// The end of a stream is where the original loses data: a block that is
// complete but has nothing after it is never emitted.
func TestBlockFlushEmitsWhatIsHeld(t *testing.T) {
	b := mustBlock(t, 5)

	// Ten samples, blocks of five: the first emits only because samples follow
	// it, and the second never would.
	out, consumed := b.Process(nil, seq(10))
	if len(out) != 1 {
		t.Fatalf("mid-stream gave %v, want one block", out)
	}
	closeAll(t, b.Flush(nil, seq(10)[consumed:]), []float64{7})
}

// A last block shorter than n is still real data, so it is averaged over what
// it has rather than dropped.
func TestBlockFlushEmitsAShortLastBlock(t *testing.T) {
	b := mustBlock(t, 4)
	// 0..6: one whole block of four, then three left over.
	closeAll(t, b.Flush(nil, seq(7)), []float64{1.5, 5})
}

func TestBlockFlushOnNothingEmitsNothing(t *testing.T) {
	if got := mustBlock(t, 3).Flush(nil, nil); len(got) != 0 {
		t.Errorf("flushing an empty block gave %v", got)
	}
}
