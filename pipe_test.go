package plot

import (
	"strings"
	"testing"
)

// mustPipeline parses a spec that the test author knows is valid.
func mustPipeline(t *testing.T, spec string) *Pipeline {
	t.Helper()
	p, err := ParsePipeline(spec)
	if err != nil {
		t.Fatalf("ParsePipeline(%q): %v", spec, err)
	}
	return p
}

// drain runs everything through a pipeline in one call.
func drain(p *Pipeline, in []float64) []float64 {
	return append([]float64(nil), p.Process(in)...)
}

// chunked runs the same data through in pieces of the given size, which is what
// a stream does.
func chunked(p *Pipeline, in []float64, size int) []float64 {
	var out []float64
	for i := 0; i < len(in); i += size {
		end := i + size
		if end > len(in) {
			end = len(in)
		}
		out = append(out, p.Process(in[i:end])...)
	}
	return out
}

// The property the whole design rests on: how the samples were divided up on
// the way in must not change what comes out. A file read in one go and a
// counter polled one sample at a time have to agree, or following a stream
// would draw something different from plotting the same numbers from a file.
func TestChunkingDoesNotChangeTheResult(t *testing.T) {
	for _, spec := range []string{"avg:5", "sma:5", "cma", "roc:1", "avg:5|roc:5", "sma:3|cma"} {
		whole, err := ParsePipeline(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		want := drain(whole, seq(60))

		for _, size := range []int{1, 2, 3, 7, 16, 59} {
			p, err := ParsePipeline(spec)
			if err != nil {
				t.Fatalf("%s: %v", spec, err)
			}
			got := chunked(p, seq(60), size)
			if len(got) != len(want) {
				t.Errorf("%s in chunks of %d gave %d values, one call gave %d",
					spec, size, len(got), len(want))
				continue
			}
			for i := range want {
				if !near(got[i], want[i]) {
					t.Errorf("%s in chunks of %d differs at %d: %v vs %v",
						spec, size, i, got[i], want[i])
					break
				}
			}
		}
	}
}

// An empty pipeline is the identity, which is what lets data take the same path
// whether or not any processing was asked for.
func TestEmptyPipelinePassesThrough(t *testing.T) {
	p, err := ParsePipeline("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Len() != 0 {
		t.Errorf("an empty spec built %d stages", p.Len())
	}
	closeAll(t, drain(p, seq(5)), seq(5))
}

// Stages run in the order written: avg first, then roc over the averages.
func TestStagesRunInOrder(t *testing.T) {
	// A counter climbing by 10 a sample. Averaging every 2 gives a series
	// climbing by 20, so its rate of change is 20 a step.
	in := make([]float64, 21)
	for i := range in {
		in[i] = float64(i * 10)
	}

	p, err := ParsePipeline("avg:2|roc:1")
	if err != nil {
		t.Fatal(err)
	}
	got := drain(p, in)
	if len(got) < 3 {
		t.Fatalf("got %d values: %v", len(got), got)
	}
	// First is zero — nothing to compare against — then the constant rate.
	if !near(got[0], 0) {
		t.Errorf("first value %v, want 0", got[0])
	}
	for i := 1; i < len(got); i++ {
		if !near(got[i], 20) {
			t.Errorf("value %d = %v, want 20 (full: %v)", i, got[i], got)
			break
		}
	}
}

// The order matters, so the reverse must not give the same answer.
func TestOrderChangesTheResult(t *testing.T) {
	in := seq(40)
	a := mustPipeline(t, "avg:3|roc:1")
	b := mustPipeline(t, "roc:1|avg:3")
	ra, rb := drain(a, in), drain(b, in)

	same := len(ra) == len(rb)
	if same {
		for i := range ra {
			if !near(ra[i], rb[i]) {
				same = false
				break
			}
		}
	}
	if same {
		t.Error("avg|roc and roc|avg gave the same result; the order is not being honored")
	}
}

// ── parsing ──────────────────────────────────────────────────────────────────

func TestParsePipelineAcceptsTheOriginalSyntax(t *testing.T) {
	for _, spec := range []string{
		"avg:5", "sma:5", "cma", "roc:5", "roc",
		"avg:5|roc:5", "sma:3|cma|roc:2",
		" avg:5 | roc:5 ", // whitespace is tolerated
	} {
		if _, err := ParsePipeline(spec); err != nil {
			t.Errorf("%q: %v", spec, err)
		}
	}
}

func TestParsePipelineCountsStages(t *testing.T) {
	for spec, want := range map[string]int{
		"":                0,
		"cma":             1,
		"avg:5|roc:5":     2,
		"sma:3|cma|roc:2": 3,
	} {
		p, err := ParsePipeline(spec)
		if err != nil {
			t.Fatalf("%q: %v", spec, err)
		}
		if p.Len() != want {
			t.Errorf("%q built %d stages, want %d", spec, p.Len(), want)
		}
	}
}

// An unusable pipeline has to be reported when it is written, not silently
// dropped — the whole point of the flag is the processing.
func TestParsePipelineRejectsWhatItCannotBuild(t *testing.T) {
	for _, spec := range []string{
		"nosuch",       // unknown processor
		"avg",          // needs an argument
		"sma",          // needs an argument
		"avg:0",        // out of range
		"avg:x",        // not a number
		"sma:4",        // even window
		"roc:0",        // would divide by zero
		"cma:5",        // takes no argument
		"avg:5|",       // empty element
		"|avg:5",       // empty element
		"avg:5||roc:1", // empty element
	} {
		if _, err := ParsePipeline(spec); err == nil {
			t.Errorf("%q was accepted", spec)
		}
	}
}

// The error names what is wrong and what the choices are, since it comes
// straight from a command line.
func TestParsePipelineErrorsAreUseful(t *testing.T) {
	_, err := ParsePipeline("nosuch")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"nosuch", "avg", "sma", "cma", "roc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	if _, err := ParsePipeline("sma:4"); err == nil || !strings.Contains(err.Error(), "odd") {
		t.Errorf("an even window should say it must be odd, got: %v", err)
	}
}

// roc without an argument defaults to an interval of one, so "roc" means
// per-sample change rather than being an error.
func TestRateDefaultsToOneSample(t *testing.T) {
	bare := mustPipeline(t, "roc")
	explicit := mustPipeline(t, "roc:1")
	in := []float64{0, 5, 15}
	closeAll(t, drain(bare, in), drain(explicit, in))
}

func TestPipelineOnNoInput(t *testing.T) {
	p := mustPipeline(t, "avg:3|roc:1")
	if got := p.Process(nil); len(got) != 0 {
		t.Errorf("empty input gave %v", got)
	}
}

// Flush has to run the last of each stage through the stages after it, or a
// pipeline would lose at its end what a single processor does not.
func TestPipelineFlushRunsThroughLaterStages(t *testing.T) {
	p := mustPipeline(t, "avg:2|roc:1")

	// avg:2 over 0 10 20 30 gives 5 and 25, but mid-stream only 5 comes out.
	mid := drain(p, []float64{0, 10, 20, 30})
	tail := p.Flush()

	// Rate must see 25 as following 5, not as the first sample of a new stream:
	// a flushed 25 arriving unaware of the 5 before it would read as zero.
	got := append(append([]float64(nil), mid...), tail...)
	closeAll(t, got, []float64{0, 20})
}

func TestEmptyPipelineFlushesNothing(t *testing.T) {
	if got := mustPipeline(t, "").Flush(); len(got) != 0 {
		t.Errorf("an empty pipeline flushed %v", got)
	}
}

// Stages that consume everything they are given have nothing left at the end.
func TestFlushAfterAConsumingStageIsEmpty(t *testing.T) {
	for _, spec := range []string{"sma:3", "cma", "roc:1"} {
		p := mustPipeline(t, spec)
		drain(p, seq(9))
		if got := p.Flush(); len(got) != 0 {
			t.Errorf("%s flushed %v, want nothing held", spec, got)
		}
	}
}
