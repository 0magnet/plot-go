package plot

import (
	"strings"
	"testing"
)

func scanned(t *testing.T, in string) []float64 {
	t.Helper()
	var out []float64
	if err := Scan(strings.NewReader(in), func(v float64) { out = append(out, v) }); err != nil {
		t.Fatalf("Scan(%q): %v", in, err)
	}
	return out
}

func TestScanReadsPlainNumbers(t *testing.T) {
	closeAll(t, scanned(t, "1\n2\n3\n"), []float64{1, 2, 3})
	closeAll(t, scanned(t, "1 2 3"), []float64{1, 2, 3})
	closeAll(t, scanned(t, "1,2,3"), []float64{1, 2, 3})
}

// The last number does not need a delimiter after it, which matters because a
// file often ends without a trailing newline.
func TestScanReadsATrailingNumber(t *testing.T) {
	closeAll(t, scanned(t, "1 2 3"), []float64{1, 2, 3})
	closeAll(t, scanned(t, "42"), []float64{42})
}

func TestScanReadsDecimalsAndExponents(t *testing.T) {
	closeAll(t, scanned(t, "1.5 0.25 1e3 2.5E-2"), []float64{1.5, 0.25, 1000, 0.025})
}

func TestScanReadsNegatives(t *testing.T) {
	closeAll(t, scanned(t, "-1 -2.5 3"), []float64{-1, -2.5, 3})
}

// This is the leniency the original is built around: point it at a log line and
// it finds the numbers without anything in between.
func TestScanPicksNumbersOutOfText(t *testing.T) {
	closeAll(t, scanned(t, "load: 1.5 ok\nload: 2.5 ok\n"), []float64{1.5, 2.5})
	closeAll(t, scanned(t, "rx=100 tx=200"), []float64{100, 200})
}

// The cost of that leniency: a hyphen in front of digits is a sign, so a date
// reads as a year and two negative numbers. The original does the same — its
// start_of_number accepts '-' and hands the rest to strtod — and plotting
// "[2026-01-01] value=7" with it bottoms the axis out at -1. Pinned here
// because it is the sort of thing that looks like a bug in a port.
func TestScanReadsADateAsSignedNumbers(t *testing.T) {
	closeAll(t, scanned(t, "[2026-01-01] value=7"), []float64{2026, -1, -1, 7})
}

func TestScanOnTextWithNoNumbers(t *testing.T) {
	if got := scanned(t, "no numbers here at all"); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
	if got := scanned(t, ""); len(got) != 0 {
		t.Errorf("empty input gave %v", got)
	}
}

// A stray sign is not a number and must not become one, nor swallow what
// follows it.
func TestScanSkipsLoneSigns(t *testing.T) {
	closeAll(t, scanned(t, "- 5"), []float64{5})
	closeAll(t, scanned(t, "-- 5"), []float64{5})
	closeAll(t, scanned(t, ". 5"), []float64{5})
}

// A hyphen between two numbers separates them rather than negating the second,
// so a range reads as two values.
func TestScanSplitsARange(t *testing.T) {
	closeAll(t, scanned(t, "3-4"), []float64{3, -4})
}

func TestScanHandlesWindowsLineEndings(t *testing.T) {
	closeAll(t, scanned(t, "1\r\n2\r\n"), []float64{1, 2})
}
