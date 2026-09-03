// Command plot-go reads numbers and draws them as a line graph.
//
// It is the CLI half of the port of https://github.com/annacrombie/plot. The
// flags follow the original where they mean the same thing, so a command line
// written for C plot mostly works here:
//
//	seq 1 99 | shuf | plot-go
//	plot-go -p "avg:5|roc:5" -c l -i /sys/class/net/eno1/statistics/rx_packets:r -f
//
// The drawing is done by github.com/guptarohit/asciigraph; what this repository
// adds is everything before it — the pipeline that turns a counter into a rate,
// and the input handling that lets a counter be polled at all.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0magnet/asciigraph"

	plot "github.com/0magnet/plot-go"
)

const usage = `usage: plot-go [opts]

opts
  -i <filename>|-        add a data source. ":r" after the name re-reads it
                         from the start each poll, which is how a counter is
                         sampled
  -p <pipeline>          processing for the next source, e.g. "avg:5|roc:5".
                         Before any -i it becomes the default for every source
  -c <color>             color of the next source
  -b [min]:[max]         fix the plot bounds
  -d [height]:[width]    plot dimensions
  -m                     merge the junctions where series cross
  -x [every]:[offset]:[mod]  x label format: one label every N columns,
                         counting from offset, wrapping at mod
  -y [width]:[prec]:[side]  y label format; only prec is used
  -t <caption>           caption printed under the plot
  -s ascii|unicode       output charset
  -f                     follow input, waiting at the end for more
  -A                     animate: follow, then exit when the input ends
  -S <milliseconds>      follow rate (default 1000)
  -h                     duh...

processors: avg:n  sma:n(odd)  cma  roc:interval
colors: black, red, green, yellow, blue, magenta, cyan, white — as the
        letters b r g y l m c w, capital for the bright variant, or by name

ex.
  seq 1 99 | shuf | plot-go -c g
  plot-go -f -S 500 -p roc:1 -i /sys/class/net/eno1/statistics/rx_bytes:r
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type opts struct {
	sources  []*plot.Source
	colors   []asciigraph.AnsiColor
	legends  []string
	height   int
	width    int
	prec     uint
	caption  string
	lower    *float64
	upper    *float64
	ascii    bool
	merge    bool
	xevery   int
	xoffset  int
	xmod     int
	follow   bool
	animate  bool
	interval time.Duration
}

// parse walks the arguments in order, because order is meaningful: -c and -p
// apply to the -i that follows them, and a -p before any -i is the default for
// all of them. Go's flag package cannot express that.
func parse(args []string) (*opts, error) {
	o := &opts{prec: 2, interval: time.Second}

	var (
		defaultPipe string
		nextPipe    string
		nextColor   = "default"
		haveDefault bool
	)

	need := func(i int, flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("plot-go: %s needs an argument", flag)
		}
		return args[i+1], nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			fmt.Print(usage)
			os.Exit(0)

		case "-f":
			o.follow = true

		case "-A":
			o.follow, o.animate = true, true

		case "-m":
			o.merge = true

		case "-i":
			name, err := need(i, "-i")
			if err != nil {
				return nil, err
			}
			i++

			spec := nextPipe
			if spec == "" {
				spec = defaultPipe
			} else if defaultPipe != "" {
				// A pipeline given before any source is prepended to the ones
				// given after, as in the original.
				spec = defaultPipe + "|" + spec
			}
			pipe, err := plot.ParsePipeline(spec)
			if err != nil {
				return nil, err
			}

			src, err := plot.OpenSource(name, pipe)
			if err != nil {
				return nil, err
			}
			color, err := parseColor(nextColor)
			if err != nil {
				return nil, err
			}

			o.sources = append(o.sources, src)
			o.colors = append(o.colors, color)
			o.legends = append(o.legends, src.Name)
			nextPipe, nextColor = "", "default"

		case "-p":
			v, err := need(i, "-p")
			if err != nil {
				return nil, err
			}
			i++
			// Before any source this is the default; after one it belongs to
			// the next source.
			if len(o.sources) == 0 && !haveDefault {
				defaultPipe, haveDefault = v, true
			} else {
				nextPipe = v
			}

		case "-c":
			v, err := need(i, "-c")
			if err != nil {
				return nil, err
			}
			i++
			nextColor = v

		case "-d", "-w", "-y", "-S", "-t", "-b", "-s", "-x":
			v, err := need(i, arg)
			if err != nil {
				return nil, err
			}
			i++
			if err := o.setValue(arg, v); err != nil {
				return nil, err
			}

		default:
			return nil, fmt.Errorf("plot-go: unknown option %q\n\n%s", arg, usage)
		}
	}

	// With no source named, stdin is the source — as in the original.
	if len(o.sources) == 0 {
		pipe, err := plot.ParsePipeline(defaultPipe)
		if err != nil {
			return nil, err
		}
		src, err := plot.OpenSource("-", pipe)
		if err != nil {
			return nil, err
		}
		color, err := parseColor(nextColor)
		if err != nil {
			return nil, err
		}
		o.sources = append(o.sources, src)
		o.colors = append(o.colors, color)
		o.legends = append(o.legends, src.Name)
	}

	// -f waits at the end of the input for more to arrive; -A stops there. The
	// flag may come after the sources it applies to, so this is settled here
	// rather than as each source is opened.
	if o.follow && !o.animate {
		for _, s := range o.sources {
			s.Infinite = true
		}
	}
	return o, nil
}

func (o *opts) setValue(flag, v string) error {
	switch flag {
	case "-t":
		o.caption = v
		return nil

	case "-s":
		switch v {
		case "ascii":
			o.ascii = true
		case "unicode":
			o.ascii = false
		default:
			return fmt.Errorf("plot-go: -s: %q is not ascii or unicode", v)
		}
		return nil

	case "-d":
		// height:width, either field may be left out.
		h, w, err := twoFields(flag, v)
		if err != nil {
			return err
		}
		if h != nil {
			o.height = int(*h)
		}
		if w != nil {
			o.width = int(*w)
		}
		return nil

	case "-b":
		// min:max, either field may be left out to leave that end automatic.
		lo, hi, err := twoFields(flag, v)
		if err != nil {
			return err
		}
		if lo != nil && hi != nil && *lo >= *hi {
			return fmt.Errorf("plot-go: -b: %v is not below %v", *lo, *hi)
		}
		o.lower, o.upper = lo, hi
		return nil

	case "-y":
		// width:prec:side in the original. asciigraph sizes the labels itself
		// and puts them on the left, so only the precision means anything here.
		fields := strings.Split(v, ":")
		spec := fields[0]
		if len(fields) > 1 {
			spec = fields[1]
		}
		if spec == "" {
			return nil
		}
		n, err := strconv.Atoi(spec)
		if err != nil {
			return fmt.Errorf("plot-go: -y: %q is not a number", spec)
		}
		if n < 0 {
			return fmt.Errorf("plot-go: -y: precision cannot be negative")
		}
		o.prec = uint(n)
		return nil

	case "-x":
		// every:offset:mod:side:color in the original. The last two are the
		// side the labels go on and the color of the wrap point, neither of
		// which asciigraph can express: it draws one labeled axis, below the
		// plot, in one color. Rejected rather than ignored, so a command line
		// that asks for them is not quietly given something else.
		fields := strings.Split(v, ":")
		if len(fields) > 3 {
			for _, f := range fields[3:] {
				if f != "" {
					return fmt.Errorf("plot-go: -x: the side and color fields are not supported; " +
						"the labels go below the plot in the axis color")
				}
			}
		}
		nums := []*int{&o.xevery, &o.xoffset, &o.xmod}
		for i, f := range fields {
			if i >= len(nums) || f == "" {
				continue
			}
			n, err := strconv.Atoi(f)
			if err != nil {
				return fmt.Errorf("plot-go: -x: %q is not a whole number", f)
			}
			if i != 1 && n < 0 {
				return fmt.Errorf("plot-go: -x: %d cannot be negative", n)
			}
			*nums[i] = n
		}
		return nil

	case "-w":
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("plot-go: -w: %q is not a number", v)
		}
		o.width = n
		return nil

	case "-S":
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("plot-go: -S: %q is not a number", v)
		}
		if n <= 0 {
			return fmt.Errorf("plot-go: -S must be above zero")
		}
		o.interval = time.Duration(n) * time.Millisecond
		return nil
	}
	return nil
}

// twoFields reads the "a:b" form the original uses for -d and -b, where either
// side may be empty to mean "leave this one alone".
func twoFields(flag, v string) (*float64, *float64, error) {
	a, b, hasColon := strings.Cut(v, ":")
	if !hasColon {
		// A bare number is the first field, so "-d 20" is a height.
		b = ""
	}
	parse := func(s string) (*float64, error) {
		if s == "" {
			return nil, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("plot-go: %s: %q is not a number", flag, s)
		}
		return &f, nil
	}
	first, err := parse(a)
	if err != nil {
		return nil, nil, err
	}
	second, err := parse(b)
	if err != nil {
		return nil, nil, err
	}
	return first, second, nil
}

// letterColors is the original's color letters, in ANSI order. A capital letter
// is the bright variant, which in the 256-color palette is the same index plus
// eight — so "r" is 1 and "R" is 9.
var letterColors = map[byte]asciigraph.AnsiColor{
	'b': 0, 'r': 1, 'g': 2, 'y': 3, 'l': 4, 'm': 5, 'c': 6, 'w': 7,
}

func parseColor(name string) (asciigraph.AnsiColor, error) {
	if name == "" || name == "default" {
		return asciigraph.Default, nil
	}

	if len(name) == 1 {
		lower := name[0] | 0x20 // ASCII lowercase
		if base, ok := letterColors[lower]; ok {
			if name[0] != lower {
				return base + 8, nil // capital: the bright variant
			}
			if base == 0 {
				// Index zero means "whatever the terminal was using", so
				// asciigraph keeps a separate value for an actual black.
				return asciigraph.Black, nil
			}
			return base, nil
		}
	}

	if c, ok := asciigraph.ColorNames[strings.ToLower(name)]; ok {
		return c, nil
	}
	return asciigraph.Default, fmt.Errorf("plot-go: unknown color %q", name)
}

func run(args []string, w io.Writer) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	defer func() {
		for _, s := range o.sources {
			_ = s.Close() //nolint:errcheck // nothing left to report it to
		}
	}()

	if o.follow {
		return follow(o, w, isTerminal(w))
	}
	return once(o, w)
}

// isTerminal reports whether w is a terminal, which decides whether redrawing
// may move the cursor. Piped into a file, the escape codes that repaint a plot
// in place are just noise between the frames.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func once(o *opts, w io.Writer) error {
	series := make([][]float64, len(o.sources))
	for i, s := range o.sources {
		got, err := s.ReadAll()
		if err != nil {
			return err
		}
		series[i] = got
	}
	out, err := render(o, series)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, out)
	return err
}

// follow polls the sources and redraws until every one of them has ended, or
// forever if any of them rewinds or was told to wait for more.
func follow(o *opts, w io.Writer, repaint bool) error {
	series := make([][]float64, len(o.sources))
	tick := time.NewTicker(o.interval)
	defer tick.Stop()

	for {
		live := false
		for i, s := range o.sources {
			if s.Done() {
				continue
			}
			live = true
			got, err := s.Read()
			if err != nil {
				return err
			}
			series[i] = append(series[i], got...)

			// Keep only what will fit, so a stream that runs for a week does
			// not hold a week of samples.
			if w := o.plotWidth(); w > 0 && len(series[i]) > w {
				series[i] = append(series[i][:0], series[i][len(series[i])-w:]...)
			}
		}

		// A render that fails has nothing to draw yet — no samples have arrived —
		// which is not a reason to stop polling. A write that fails is: whatever
		// was being drawn to has gone away.
		if out, rerr := render(o, series); rerr == nil {
			if repaint {
				// Home the cursor and paint over what was there, rather than
				// scrolling: the plot should sit still while its contents move.
				if _, err := fmt.Fprint(w, "\033[H\033[J"); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(w, out); err != nil {
				return err
			}
		}

		if !live {
			return nil
		}
		<-tick.C
	}
}

// plotWidth is how many samples fit across the plot.
func (o *opts) plotWidth() int {
	if o.width > 0 {
		return o.width
	}
	return 0
}

func render(o *opts, series [][]float64) (string, error) {
	any := false
	for _, s := range series {
		if len(s) > 0 {
			any = true
			break
		}
	}
	if !any {
		return "", fmt.Errorf("plot-go: no numbers to plot")
	}

	opts := []asciigraph.Option{
		asciigraph.Precision(o.prec),
		asciigraph.SeriesColors(o.colors...),
	}
	if o.height > 0 {
		opts = append(opts, asciigraph.Height(o.height))
	}
	if o.width > 0 {
		opts = append(opts, asciigraph.Width(o.width))
	}
	if o.caption != "" {
		opts = append(opts, asciigraph.Caption(o.caption))
	}
	if o.lower != nil {
		opts = append(opts, asciigraph.LowerBound(*o.lower))
	}
	if o.upper != nil {
		opts = append(opts, asciigraph.UpperBound(*o.upper))
	}
	if o.merge {
		opts = append(opts, asciigraph.MergeSeries())
	}
	if o.xevery > 0 {
		// The original labels columns: one label every N of them, counting from
		// an offset. asciigraph maps a range across the width instead, so the
		// range is the column numbers themselves and the tick count is how many
		// labels fit.
		widest := 0
		for _, s := range series {
			if len(s) > widest {
				widest = len(s)
			}
		}
		if o.width > 0 {
			widest = o.width
		}
		if widest > 0 {
			last := o.xoffset + widest - 1
			// asciigraph spreads n ticks evenly, putting the ith at column
			// round((width-1) * i/(n-1)). Asking for this many puts them every
			// xevery columns, exactly when xevery divides the width and as near
			// as the columns allow when it does not.
			ticks := (widest-1)/o.xevery + 1
			if ticks < 2 {
				ticks = 2
			}
			opts = append(opts,
				asciigraph.XAxisRange(float64(o.xoffset), float64(last)),
				asciigraph.XAxisTickCount(ticks),
				asciigraph.XAxisValueFormatter(func(v float64) string {
					return strconv.Itoa(wrapLabel(int(v), o.xmod))
				}))
		}
	}
	if o.ascii {
		sets := make([]asciigraph.CharSet, len(series))
		for i := range sets {
			sets[i] = asciiChars
		}
		opts = append(opts, asciigraph.SeriesChars(sets...))
	}
	if len(series) > 1 {
		opts = append(opts, asciigraph.SeriesLegends(o.legends...))
	}

	out := asciigraph.PlotMany(series, opts...)
	if o.ascii {
		// The charset covers the line but not the axis, which asciigraph draws
		// itself. With the line already ASCII, whatever box drawing is left
		// belongs to the axis.
		out = asciiAxis.Replace(out)
	}
	return out, nil
}

// asciiChars draws the line without box-drawing characters, in the shapes the
// original uses: a slope is dashes, the top of a curve an apostrophe and the
// bottom a period.
var asciiChars = asciigraph.CharSet{
	Horizontal:     "-",
	VerticalLine:   "|",
	ArcDownRight:   ".",
	ArcDownLeft:    ".",
	ArcUpRight:     "'",
	ArcUpLeft:      "'",
	EndCap:         "-",
	StartCap:       "-",
	UpRight:        "'",
	DownHorizontal: ".",
}

var asciiAxis = strings.NewReplacer(
	"┤", "|",
	"┼", "+",
	"│", "|",
	"─", "-",
	"╭", ".",
	"╮", ".",
	"╰", "'",
	"╯", "'",
	"┬", ".",
	"└", "'",
	"╴", "-",
	"╶", "-",
	"■", "#",
)

// wrapLabel applies the original's label modulus: a column number counts up to
// mod and starts again, which is what turns a column index into a clock. A mod
// of zero leaves the number alone.
//
// The sign handling is the original's too. It takes the remainder of a negative
// index and flips it, so the columns before the offset count upwards away from
// it rather than downwards.
func wrapLabel(i, mod int) int {
	if mod <= 0 {
		return i
	}
	r := i % mod
	if i < 0 {
		r = -r
	}
	return r
}
