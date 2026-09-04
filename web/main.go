//go:build js && wasm

// Command web is the plot-go demo: live counters, plotted as they arrive.
//
// The point of plot is following something that moves — `plotnet` watches
// /sys/class/net/…/statistics/{rx,tx}_packets and plots the rate. A browser
// tab has no /sys, but it does have counters of its own that move for the
// same reason: work arriving faster than it can be done. Frame interval and
// event-loop lag both rise the moment the tab is busy, and both are there in
// every browser without asking permission for anything.
//
// The picture is drawn into a terminal — xterm-go, the one websh runs on —
// because asciigraph's output *is* a terminal frame: colors are SGR escapes
// and the redraw is a cursor-home and a clear. Rendering it anywhere else
// means reimplementing a terminal badly.
//
// Everything between the samples and the picture is this repository's:
// ParsePipeline reads the same `avg:5|roc:5` spec the command line takes, and
// the processors are the same streaming ones.
package main

import (
	"strconv"
	"strings"
	"syscall/js"

	"github.com/0magnet/asciigraph"
	xterm "github.com/0magnet/xterm-go"
	"github.com/0magnet/xterm-go/vt"

	plot "github.com/0magnet/plot-go"
)

// keep is how many processed points stay on screen. The plot is redrawn from
// this window each tick, so it is also the width of the history.
const keep = 400

type series struct {
	name  string
	color asciigraph.AnsiColor
	out   []float64 // processed points, newest last
	pipe  *plot.Pipeline
}

func (s *series) reset(spec string) error {
	p, err := plot.ParsePipeline(spec)
	if err != nil {
		return err
	}
	s.pipe, s.out = p, nil
	return nil
}

// push feeds one sample through the pipeline and trims the window. A stage
// filling a window emits nothing yet, which is why one sample in is not one
// point out; the pipeline holds what it is still waiting on.
func (s *series) push(v float64) {
	// Process reuses its slice, so what comes back has to be copied out.
	s.out = append(s.out, s.pipe.Process([]float64{v})...)
	if len(s.out) > keep {
		s.out = s.out[len(s.out)-keep:]
	}
}

func main() {
	doc := js.Global().Get("document")
	if boot := doc.Call("getElementById", "boot"); boot.Truthy() {
		boot.Call("remove")
	}

	opts := vt.NewOptions()
	opts.Scrollback = 0 // a live plot has no history worth keeping
	opts.FontSize = 13
	term := xterm.New(opts)
	term.Open(doc.Call("getElementById", "term"))
	term.Fit()
	if err := term.EnableWebGL(); err != nil {
		// The DOM renderer draws the same cells, just slower; nothing here
		// needs the GPU.
		_ = err
	}
	js.Global().Call("addEventListener", "resize", js.FuncOf(func(js.Value, []js.Value) any {
		term.Fit()
		return nil
	}))

	frame := &series{name: "frame interval ms", color: asciigraph.Green}
	lag := &series{name: "event-loop lag ms", color: asciigraph.Blue}
	all := []*series{frame, lag}

	// The pipeline comes from ?p=, so the page itself stays nothing but the
	// terminal: ?p=roc:5 or ?p=avg:5|roc:5 the way the command line takes it.
	spec := "avg:5"
	if q := js.Global().Get("URLSearchParams").New(
		js.Global().Get("location").Get("search")); q.Truthy() {
		if v := q.Call("get", "p"); v.Type() == js.TypeString && v.String() != "" {
			spec = v.String()
		}
	}
	specErr := ""
	for _, s := range all {
		if err := s.reset(spec); err != nil {
			specErr = err.Error()
			s.reset("") //nolint:errcheck // the empty spec cannot fail
		}
	}

	perf := js.Global().Get("performance")

	// Frame interval: the gap between animation frames, which is what the tab
	// actually managed rather than what it was aiming for.
	var last float64
	var raf js.Func
	raf = js.FuncOf(func(_ js.Value, args []js.Value) any {
		t := args[0].Float()
		if last != 0 {
			frame.push(t - last)
		}
		last = t
		js.Global().Call("requestAnimationFrame", raf)
		return nil
	})
	js.Global().Call("requestAnimationFrame", raf)

	// Event-loop lag: ask for a timeout of zero and measure how late it is.
	// Anything blocking the loop shows up here and nowhere else.
	var tick js.Func
	tick = js.FuncOf(func(js.Value, []js.Value) any {
		start := perf.Call("now").Float()
		js.Global().Call("setTimeout", js.FuncOf(func(js.Value, []js.Value) any {
			lag.push(perf.Call("now").Float() - start)
			js.Global().Call("setTimeout", tick, 100)
			return nil
		}), 0)
		return nil
	})
	js.Global().Call("setTimeout", tick, 100)

	draw := func() {
		cols, rows := term.Core.Cols(), term.Core.Rows()
		if cols < 20 || rows < 8 {
			return
		}
		// One line for the caption asciigraph draws, one for the status line
		// below it, and one so the frame never scrolls.
		h := rows - 3
		if h < 6 {
			h = 6
		}
		width := cols - 12 // room for the axis labels asciigraph puts on the left

		var data [][]float64
		var colors []asciigraph.AnsiColor
		var names []string
		for _, s := range all {
			if len(s.out) < 2 {
				continue
			}
			pts := s.out
			if width > 0 && len(pts) > width {
				pts = pts[len(pts)-width:]
			}
			data = append(data, pts)
			colors = append(colors, s.color)
			names = append(names, s.name)
		}
		if len(data) == 0 {
			return
		}
		g := asciigraph.PlotMany(data,
			asciigraph.Height(h),
			asciigraph.SeriesColors(colors...),
			asciigraph.Caption(strings.Join(names, "   ·   ")),
		)
		// Home, clear, then the frame. A terminal redraw, which is what this
		// output was always meant for.
		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		b.WriteString(strings.ReplaceAll(g, "\n", "\r\n"))
		b.WriteString("\r\n\x1b[2m")
		if specErr != "" {
			b.WriteString("\x1b[0m\x1b[31m" + specErr + "\x1b[0m\x1b[2m — ")
		}
		b.WriteString(spec + " · " + strconv.Itoa(len(frame.out)) + " points")
		b.WriteString("\x1b[0m")
		term.WriteString(b.String())
	}

	js.Global().Call("setInterval", js.FuncOf(func(js.Value, []js.Value) any {
		draw()
		return nil
	}), 250)

	select {}
}
