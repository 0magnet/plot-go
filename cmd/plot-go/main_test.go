package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0magnet/asciigraph"
)

func tmpNums(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nums")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustParse(t *testing.T, args ...string) *opts {
	t.Helper()
	o, err := parse(args)
	if err != nil {
		t.Fatalf("parse(%v): %v", args, err)
	}
	t.Cleanup(func() {
		for _, s := range o.sources {
			_ = s.Close() //nolint:errcheck
		}
	})
	return o
}

// The whole reason for a hand-written parser: -c colors the source that comes
// after it, not the one before, and not all of them.
func TestColorAppliesToTheNextSource(t *testing.T) {
	a, b := tmpNums(t, "1 2\n"), tmpNums(t, "3 4\n")
	o := mustParse(t, "-c", "red", "-i", a, "-c", "blue", "-i", b)

	if len(o.sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(o.sources))
	}
	if o.colors[0] != asciigraph.Red {
		t.Errorf("first source is %v, want red", o.colors[0])
	}
	if o.colors[1] != asciigraph.Blue {
		t.Errorf("second source is %v, want blue", o.colors[1])
	}
}

// A source with no -c before it is not colored by the -c of the source before.
func TestColorDoesNotLeakToTheSourceAfter(t *testing.T) {
	a, b := tmpNums(t, "1\n"), tmpNums(t, "2\n")
	o := mustParse(t, "-c", "red", "-i", a, "-i", b)

	if o.colors[0] != asciigraph.Red {
		t.Errorf("first source is %v, want red", o.colors[0])
	}
	if o.colors[1] != asciigraph.Default {
		t.Errorf("second source inherited %v, want the default", o.colors[1])
	}
}

// A pipeline before any source is the default for every source after it.
func TestPipelineBeforeAnySourceIsTheDefault(t *testing.T) {
	// 0 10 20 through roc:1 is 0 10 10 — a counter climbing by ten.
	a, b := tmpNums(t, "0 10 20\n"), tmpNums(t, "0 5 10\n")
	o := mustParse(t, "-p", "roc:1", "-i", a, "-i", b)

	got := drainSources(t, o)
	wantA, wantB := []float64{0, 10, 10}, []float64{0, 5, 5}
	if !sameNums(got[0], wantA) {
		t.Errorf("first source gave %v, want %v", got[0], wantA)
	}
	if !sameNums(got[1], wantB) {
		t.Errorf("second source gave %v, want %v — the default did not reach it", got[1], wantB)
	}
}

// A pipeline after a source belongs to the next source only.
func TestPipelineAfterASourceIsPerSource(t *testing.T) {
	a, b := tmpNums(t, "0 10 20\n"), tmpNums(t, "0 10 20\n")
	o := mustParse(t, "-i", a, "-p", "roc:1", "-i", b)

	got := drainSources(t, o)
	if !sameNums(got[0], []float64{0, 10, 20}) {
		t.Errorf("first source gave %v, want the numbers untouched", got[0])
	}
	if !sameNums(got[1], []float64{0, 10, 10}) {
		t.Errorf("second source gave %v, want the rate", got[1])
	}
}

// A default pipeline and a per-source one compose, default first.
func TestDefaultAndPerSourcePipelinesCompose(t *testing.T) {
	// Six samples, averaged in pairs, then differenced: avg gives 5, 25, 45 and
	// roc turns that into 0, 20, 20.
	a := tmpNums(t, "0 10 20 30 40 50\n")
	o := mustParse(t, "-p", "avg:2", "-i", a, "-p", "roc:1", "-i", a)

	got := drainSources(t, o)
	if !sameNums(got[0], []float64{5, 25, 45}) {
		t.Errorf("plain default gave %v, want the averages", got[0])
	}
	if !sameNums(got[1], []float64{0, 20, 20}) {
		t.Errorf("composed gave %v, want avg then roc", got[1])
	}
}

func TestStdinIsTheSourceWhenNoneIsNamed(t *testing.T) {
	o := mustParse(t)
	if len(o.sources) != 1 {
		t.Fatalf("got %d sources, want the implicit stdin", len(o.sources))
	}
	if o.sources[0].Name != "stdin" {
		t.Errorf("the implicit source is %q, want stdin", o.sources[0].Name)
	}
}

// A default pipeline still applies when the source is the implicit stdin.
func TestDefaultPipelineReachesImplicitStdin(t *testing.T) {
	o := mustParse(t, "-p", "roc:1")
	if o.sources[0].Name != "stdin" {
		t.Fatalf("source is %q", o.sources[0].Name)
	}
	if n := o.sources[0].Pipeline().Len(); n != 1 {
		t.Errorf("stdin got %d processors, want the default pipeline's 1", n)
	}
}

func TestNumericFlags(t *testing.T) {
	o := mustParse(t, "-d", "12", "-w", "80", "-y", "4", "-S", "250", "-t", "hello")
	if o.height != 12 || o.width != 80 || o.prec != 4 {
		t.Errorf("got height %d width %d prec %d", o.height, o.width, o.prec)
	}
	if o.interval.Milliseconds() != 250 {
		t.Errorf("interval is %v, want 250ms", o.interval)
	}
	if o.caption != "hello" {
		t.Errorf("caption is %q", o.caption)
	}
	if o.follow {
		t.Error("follow was set without -f")
	}
}

func TestFollowFlag(t *testing.T) {
	if o := mustParse(t, "-f"); !o.follow {
		t.Error("-f did not set follow")
	}
}

func TestBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-i"},                      // no filename
		{"-p"},                      // no pipeline
		{"-c"},                      // no color
		{"-d"},                      // no number
		{"-d", "tall"},              // not a number
		{"-y", "-1"},                // negative precision
		{"-S", "0"},                 // an interval of zero would spin
		{"-c", "octarine"},          // no such color
		{"-p", "nope:1", "-i", "-"}, // no such processor
		{"--wat"},                   // no such flag
		{"-i", "/nonexistent/no"},   // no such file
	} {
		if _, err := parse(args); err == nil {
			t.Errorf("parse(%v) was accepted", args)
		}
	}
}

// The original's single letters have to keep working, alongside real names. A
// lowercase letter is the plain color and a capital is the bright one, which in
// the palette is eight higher.
func TestColorNames(t *testing.T) {
	for name, want := range map[string]asciigraph.AnsiColor{
		"r":       1,
		"R":       9,
		"l":       4, // l, not b: b is black in the original
		"L":       12,
		"g":       2,
		"G":       10,
		"w":       7,
		"W":       15,
		"b":       asciigraph.Black, // index 0 is "unchanged", not black
		"B":       8,
		"red":     asciigraph.Red,
		"blue":    asciigraph.Blue,
		"default": asciigraph.Default,
		"":        asciigraph.Default,
	} {
		got, err := parseColor(name)
		if err != nil {
			t.Errorf("parseColor(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("parseColor(%q) = %v, want %v", name, got, want)
		}
	}
	// asciigraph knows a long list of color names, so the one that must be
	// rejected has to be a color it has never heard of.
	if _, err := parseColor("octarine"); err == nil {
		t.Error("an invented color was accepted")
	}
}

func TestRenderRefusesAnEmptySeries(t *testing.T) {
	o := &opts{prec: 2, colors: []asciigraph.AnsiColor{asciigraph.Default}}
	if _, err := render(o, [][]float64{{}}); err == nil {
		t.Error("rendering nothing did not fail")
	}
}

func TestRenderLabelsMultipleSeries(t *testing.T) {
	o := &opts{
		prec:    2,
		colors:  []asciigraph.AnsiColor{asciigraph.Default, asciigraph.Default},
		legends: []string{"first", "second"},
	}
	out, err := render(o, [][]float64{{1, 2, 3}, {3, 2, 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(out, want) {
			t.Errorf("the legend %q is missing from:\n%s", want, out)
		}
	}
}

// One series gets no legend — a label on a lone line is noise.
func TestRenderOmitsTheLegendForOneSeries(t *testing.T) {
	o := &opts{prec: 2, colors: []asciigraph.AnsiColor{asciigraph.Default}, legends: []string{"only"}}
	out, err := render(o, [][]float64{{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "only") {
		t.Errorf("a lone series was labeled:\n%s", out)
	}
}

func TestRenderHonoursTheCaption(t *testing.T) {
	o := &opts{prec: 2, colors: []asciigraph.AnsiColor{asciigraph.Default}, caption: "throughput"}
	out, err := render(o, [][]float64{{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "throughput") {
		t.Errorf("the caption is missing from:\n%s", out)
	}
}

func drainSources(t *testing.T, o *opts) [][]float64 {
	t.Helper()
	out := make([][]float64, len(o.sources))
	for i, s := range o.sources {
		got, err := s.ReadAll()
		if err != nil {
			t.Fatalf("reading %s: %v", s.Name, err)
		}
		out[i] = got
	}
	return out
}

func sameNums(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if d := got[i] - want[i]; d > 1e-9 || d < -1e-9 {
			return false
		}
	}
	return true
}

// The original packs both dimensions into one flag, so a command line written
// for it has to mean the same thing here.
func TestDimensionsFlag(t *testing.T) {
	for _, tc := range []struct {
		arg           string
		height, width int
	}{
		{"20:80", 20, 80},
		{"20", 20, 0},  // a bare number is the height
		{"20:", 20, 0}, // an empty field leaves that dimension automatic
		{":80", 0, 80}, // width only
	} {
		o := mustParse(t, "-d", tc.arg)
		if o.height != tc.height || o.width != tc.width {
			t.Errorf("-d %q gave height %d width %d, want %d and %d",
				tc.arg, o.height, o.width, tc.height, tc.width)
		}
	}
}

func TestBoundsFlag(t *testing.T) {
	o := mustParse(t, "-b", "0:100")
	if o.lower == nil || *o.lower != 0 {
		t.Errorf("lower bound is %v, want 0", o.lower)
	}
	if o.upper == nil || *o.upper != 100 {
		t.Errorf("upper bound is %v, want 100", o.upper)
	}

	// One end may be left automatic.
	o = mustParse(t, "-b", "0:")
	if o.lower == nil || *o.lower != 0 {
		t.Errorf("lower bound is %v, want 0", o.lower)
	}
	if o.upper != nil {
		t.Errorf("upper bound is %v, want it left automatic", *o.upper)
	}
}

// A plot whose bounds are the wrong way round has no sensible rendering, so it
// is refused rather than drawn strangely.
func TestBoundsMustBeOrdered(t *testing.T) {
	if _, err := parse([]string{"-b", "100:0"}); err == nil {
		t.Error("inverted bounds were accepted")
	}
}

// The bounds have to reach the plot, not just the options struct.
func TestBoundsReachTheRender(t *testing.T) {
	o := &opts{prec: 2, colors: []asciigraph.AnsiColor{asciigraph.Default}}
	plain, err := render(o, [][]float64{{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	hundred := 100.0
	o.upper = &hundred
	bounded, err := render(o, [][]float64{{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if plain == bounded {
		t.Errorf("an upper bound of 100 changed nothing:\n%s", bounded)
	}
	if !strings.Contains(bounded, "100.00") {
		t.Errorf("the bound is missing from the axis:\n%s", bounded)
	}
}

// The y flag's original form is width:prec:side; only the precision applies.
func TestYFlagTakesThePrecisionField(t *testing.T) {
	if o := mustParse(t, "-y", "3"); o.prec != 3 {
		t.Errorf("-y 3 gave precision %d", o.prec)
	}
	if o := mustParse(t, "-y", "8:4:left"); o.prec != 4 {
		t.Errorf("-y 8:4:left gave precision %d, want the second field", o.prec)
	}
	if o := mustParse(t, "-y", "8::left"); o.prec != 2 {
		t.Errorf("-y with no precision field gave %d, want the default", o.prec)
	}
}

// -f waits at the end of the input for more; -A stops there. That difference is
// carried by the sources, so it has to survive the flag coming after them.
func TestFollowAndAnimateDifferAtEndOfInput(t *testing.T) {
	path := tmpNums(t, "1 2 3\n")

	o := mustParse(t, "-i", path, "-f")
	if !o.sources[0].Infinite {
		t.Error("-f did not make the source wait at the end")
	}

	o = mustParse(t, "-i", path, "-A")
	if !o.follow || !o.animate {
		t.Errorf("-A gave follow=%v animate=%v", o.follow, o.animate)
	}
	if o.sources[0].Infinite {
		t.Error("-A made the source wait at the end, so it would never exit")
	}
}

func TestAsciiCharset(t *testing.T) {
	o := mustParse(t, "-s", "ascii")
	if !o.ascii {
		t.Fatal("-s ascii did not take")
	}
	out, err := render(&opts{
		prec:   2,
		ascii:  true,
		colors: []asciigraph.AnsiColor{asciigraph.Default},
	}, [][]float64{{1, 5, 2, 8}})
	if err != nil {
		t.Fatal(err)
	}
	for _, box := range []string{"╭", "╮", "╰", "╯", "─"} {
		if strings.Contains(out, box) {
			t.Errorf("box drawing %q survived -s ascii:\n%s", box, out)
		}
	}
	if _, err := parse([]string{"-s", "ebcdic"}); err == nil {
		t.Error("an unknown charset was accepted")
	}
}

func TestRunDrawsOnce(t *testing.T) {
	var buf strings.Builder
	if err := run([]string{"-i", tmpNums(t, "0 10 20 30\n"), "-d", "4"}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "30.00") || !strings.Contains(out, "0.00") {
		t.Errorf("the plot does not span the data:\n%s", out)
	}
	if strings.Count(out, "\n") < 4 {
		t.Errorf("wanted four rows, got:\n%s", out)
	}
}

// -A follows and then stops, so it has to terminate on a file that ends. If the
// end-of-input handling is wrong this test hangs rather than fails.
func TestAnimateStopsAtEndOfInput(t *testing.T) {
	var buf strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-i", tmpNums(t, "1 2 3 4 5\n"), "-A", "-S", "1", "-d", "3"}, &buf)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("-A never returned; it is waiting for input that has ended")
	}
	if !strings.Contains(buf.String(), "5.00") {
		t.Errorf("the last sample never made it to the plot:\n%s", buf.String())
	}
}

// Piped rather than displayed, the cursor codes that repaint in place are noise.
func TestFollowDoesNotRepaintWhenPiped(t *testing.T) {
	var buf strings.Builder
	if err := run([]string{"-i", tmpNums(t, "1 2 3\n"), "-A", "-S", "1", "-d", "3"}, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\033[H") {
		t.Error("a cursor-home escape was written to something that is not a terminal")
	}
}

func TestRunReportsBadArguments(t *testing.T) {
	if err := run([]string{"--nope"}, io.Discard); err == nil {
		t.Error("a bad argument did not come back as an error")
	}
}

// A stream that runs for a long time must not accumulate samples without limit;
// with a width set, only what fits is kept.
func TestFollowKeepsOnlyWhatFits(t *testing.T) {
	var buf strings.Builder
	nums := make([]string, 500)
	for i := range nums {
		nums[i] = strconv.Itoa(i)
	}
	err := run([]string{
		"-i", tmpNums(t, strings.Join(nums, " ")),
		"-A", "-S", "1", "-d", "3", "-w", "20",
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	// The tail of the data is what should be on screen at the end.
	if !strings.Contains(buf.String(), "499.00") {
		t.Errorf("the newest sample is not in the last frame:\n%s", lastFrame(buf.String()))
	}
}

func lastFrame(s string) string {
	frames := strings.Split(strings.TrimRight(s, "\n"), "\n\n")
	return frames[len(frames)-1]
}

// If whatever the plot is being written to goes away, following it stops. A
// broken pipe that was ignored would leave it polling forever with nowhere to
// draw.
func TestFollowStopsWhenTheOutputGoesAway(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-i", tmpNums(t, "1 2 3\n"), "-f", "-S", "1"}, brokenPipe{})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a failed write was not reported")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("following did not stop when the output failed")
	}
}

type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// Two series crossing overwrite each other unless merging is on, which is what
// the fork of asciigraph adds.
func TestMergeJoinsCrossings(t *testing.T) {
	data := [][]float64{{0, 1}, {1, 0}}
	o := &opts{prec: 2, colors: []asciigraph.AnsiColor{asciigraph.Default, asciigraph.Default}}

	plain, err := render(o, data)
	if err != nil {
		t.Fatal(err)
	}
	o.merge = true
	merged, err := render(o, data)
	if err != nil {
		t.Fatal(err)
	}

	if plain == merged {
		t.Errorf("-m changed nothing:\n%s", merged)
	}
	for _, want := range []string{"┬", "┴"} {
		if !strings.Contains(merged, want) {
			t.Errorf("expected a %q junction in:\n%s", want, merged)
		}
	}
	if _, err := parse([]string{"-m"}); err != nil {
		t.Errorf("-m was rejected: %v", err)
	}
}

// The original labels columns rather than a range: one every N, counting from
// an offset, optionally wrapping.
func TestXLabelFlag(t *testing.T) {
	o := mustParse(t, "-x", "10:5:60")
	if o.xevery != 10 || o.xoffset != 5 || o.xmod != 60 {
		t.Errorf("got every %d offset %d mod %d", o.xevery, o.xoffset, o.xmod)
	}

	// Fields may be left out.
	o = mustParse(t, "-x", "4")
	if o.xevery != 4 || o.xoffset != 0 || o.xmod != 0 {
		t.Errorf("bare every gave %d %d %d", o.xevery, o.xoffset, o.xmod)
	}

	// A negative offset is meaningful — columns before the start — but a
	// negative spacing or modulus is not.
	if _, err := parse([]string{"-x", "10:-5:60"}); err != nil {
		t.Errorf("a negative offset was rejected: %v", err)
	}
	for _, bad := range []string{"-1", "10:0:-3", "ten"} {
		if _, err := parse([]string{"-x", bad}); err == nil {
			t.Errorf("-x %q was accepted", bad)
		}
	}
}

// The side and color fields have no equivalent, so they are refused rather than
// quietly dropped: a command line that asks for labels above the plot should
// not silently get them below.
func TestXLabelRejectsUnsupportedFields(t *testing.T) {
	for _, spec := range []string{"5:0:0:top", "5:0:0::red", "5:0:0:bottom"} {
		if _, err := parse([]string{"-x", spec}); err == nil {
			t.Errorf("-x %q was accepted", spec)
		}
	}
	// Trailing empty fields are not a request for anything.
	if _, err := parse([]string{"-x", "5:0:0:"}); err != nil {
		t.Errorf("-x with an empty side field was rejected: %v", err)
	}
}

func TestXLabelsAreDrawn(t *testing.T) {
	o := &opts{prec: 0, colors: []asciigraph.AnsiColor{asciigraph.Default}, xevery: 10}
	series := make([]float64, 41)
	for i := range series {
		series[i] = float64(i)
	}
	out, err := render(o, [][]float64{series})
	if err != nil {
		t.Fatal(err)
	}
	// Every tenth column from zero, which is exactly what the original draws
	// when the spacing divides the width.
	for _, want := range []string{"0", "10", "20", "30", "40"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected the label %q in:\n%s", want, out)
		}
	}
}

func TestLabelWrapping(t *testing.T) {
	for _, tc := range []struct{ i, mod, want int }{
		{0, 60, 0},
		{59, 60, 59},
		{60, 60, 0},
		{61, 60, 1},
		{61, 0, 61},  // no modulus leaves the number alone
		{61, -1, 61}, // nor does a nonsensical one
		// A negative column has its remainder flipped, as in the original, so
		// the columns before the offset count away from it.
		{-1, 60, 1},
		{-61, 60, 1},
	} {
		if got := wrapLabel(tc.i, tc.mod); got != tc.want {
			t.Errorf("wrapLabel(%d, %d) = %d, want %d", tc.i, tc.mod, got, tc.want)
		}
	}
}
