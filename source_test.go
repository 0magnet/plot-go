package plot

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestSourceReadsAFile(t *testing.T) {
	path := writeFile(t, t.TempDir(), "nums", "1 2 3\n4 5\n")
	s, err := OpenSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	got, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	closeAll(t, got, []float64{1, 2, 3, 4, 5})
	if !s.Done() {
		t.Error("a file read to the end is not marked done")
	}
}

// A counter under /sys is one number that changes in place. Reading to the end
// and waiting for more would wait forever; the value has to be read again from
// the top, which is what the rewind flag is for.
func TestRewindReadsAChangingFileAgain(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "counter", "100\n")

	s, err := OpenSource(path+":r", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	if !s.Rewind {
		t.Fatal("the r flag did not set Rewind")
	}

	var seen []float64
	for _, v := range []string{"100\n", "150\n", "225\n"} {
		writeFile(t, dir, "counter", v)
		// Two reads: one reaches the end and rewinds, the next sees the value.
		for i := 0; i < 2; i++ {
			got, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, got...)
		}
		if s.Done() {
			t.Fatal("a rewinding source marked itself done")
		}
	}

	if len(seen) < 3 {
		t.Fatalf("polled three values, saw %v", seen)
	}
	// The newest value must be the last one written.
	if !near(seen[len(seen)-1], 225) {
		t.Errorf("last value %v, want the 225 the file now holds (all: %v)", seen[len(seen)-1], seen)
	}
}

// Rewinding turns a counter into a rate when the pipeline says so — this is
// what plotnet does, in miniature.
func TestRewindWithARatePipeline(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "counter", "1000\n")

	pipe, err := ParsePipeline("roc:1")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSource(path+":r", pipe)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	var rates []float64
	for _, v := range []string{"1000\n", "1010\n", "1020\n", "1030\n"} {
		writeFile(t, dir, "counter", v)
		for i := 0; i < 2; i++ {
			got, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			rates = append(rates, got...)
		}
	}

	// A counter climbing by ten a poll is a rate of ten, whatever the counter
	// itself reads.
	if len(rates) < 2 {
		t.Fatalf("got %v", rates)
	}
	last := rates[len(rates)-1]
	if !near(last, 10) {
		t.Errorf("rate %v, want 10 (all: %v)", last, rates)
	}
}

// Without the flag a source that ends is finished, which is what makes reading
// a file terminate.
func TestWithoutRewindASourceEnds(t *testing.T) {
	path := writeFile(t, t.TempDir(), "nums", "7\n")
	s, err := OpenSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	for i := 0; i < 5 && !s.Done(); i++ {
		if _, err := s.Read(); err != nil {
			t.Fatal(err)
		}
	}
	if !s.Done() {
		t.Error("a plain source never finished")
	}
	got, err := s.Read()
	if err != nil || len(got) != 0 {
		t.Errorf("reading past the end gave %v, %v", got, err)
	}
}

// A number split across two reads has to survive the join, or a long file
// grows numbers that were never in it.
func TestNumbersSplitAcrossReadsSurvive(t *testing.T) {
	// A chunk boundary lands inside "12345" whatever the buffer size is if the
	// reader hands over one byte at a time.
	s := NewSource("test", &iotest1{s: "11 22 12345 33"}, nil)
	got, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	closeAll(t, got, []float64{11, 22, 12345, 33})
}

// iotest1 is a reader that returns one byte per Read, which is the worst case
// for anything that has to reassemble a number.
type iotest1 struct {
	s string
	i int
}

func (r *iotest1) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	p[0] = r.s[r.i]
	r.i++
	return 1, nil
}

func TestOpenSourceRejectsBadSpecs(t *testing.T) {
	if _, err := OpenSource("/nonexistent/nope", nil); err == nil {
		t.Error("a missing file was opened")
	}
	path := writeFile(t, t.TempDir(), "nums", "1\n")
	if _, err := OpenSource(path+":q", nil); err == nil {
		t.Error("an unknown flag was accepted")
	}
	if _, err := OpenSource("-:r", nil); err == nil {
		t.Error("stdin was accepted as rewindable")
	}
}

// The n flag is accepted so a command line written for the original still
// runs, even though polling never blocks here.
func TestOpenSourceAcceptsTheNonblockingFlag(t *testing.T) {
	path := writeFile(t, t.TempDir(), "nums", "1\n")
	if _, err := OpenSource(path+":n", nil); err != nil {
		t.Errorf("the n flag was rejected: %v", err)
	}
	if _, err := OpenSource(path+":rn", nil); err != nil {
		t.Errorf("combined flags were rejected: %v", err)
	}
}

func TestSourceAppliesItsPipeline(t *testing.T) {
	path := writeFile(t, t.TempDir(), "nums", "0 10 20 30\n")
	pipe, err := ParsePipeline("roc:1")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSource(path, pipe)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	got, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	closeAll(t, got, []float64{0, 10, 10, 10})
}

func TestSourceOnAnEmptyFile(t *testing.T) {
	path := writeFile(t, t.TempDir(), "empty", "")
	s, err := OpenSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	got, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an empty file produced %v", got)
	}
}

func TestSourceFromAReader(t *testing.T) {
	s := NewSource("test", strings.NewReader("1 2 3"), nil)
	got, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	closeAll(t, got, []float64{1, 2, 3})
	if err := s.Close(); err != nil {
		t.Errorf("closing a reader-backed source: %v", err)
	}
}

// Reading a file to the end must not lose the tail of it: a window still
// filling when the file runs out holds real samples.
func TestSourceFlushesAtEndOfFile(t *testing.T) {
	path := writeFile(t, t.TempDir(), "nums", "0 10 20 30 40 50\n")
	pipe, err := ParsePipeline("avg:2")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSource(path, pipe)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	got, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// Three pairs, three averages. The original gives two: the last block has
	// nothing after it to push it out.
	closeAll(t, got, []float64{5, 25, 45})
}

// A rewinding source has not ended, so nothing is flushed: the block boundary
// carries across the rewind, as it does across any other read.
func TestRewindDoesNotFlush(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "counter", "10\n")

	pipe, err := ParsePipeline("avg:2")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSource(path+":r", pipe)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	// One sample per poll and a block of two: the first poll cannot complete a
	// block, and must not pretend it has by flushing a block of one.
	got, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a rewinding source flushed a half-full block: %v", got)
	}
	if s.Done() {
		t.Error("a rewinding source marked itself done")
	}
}
