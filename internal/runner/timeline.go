package runner

import (
	"fmt"
	"html"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/coolstack-coder/nebulachaos/internal/checker"
	"github.com/coolstack-coder/nebulachaos/internal/history"
	"github.com/coolstack-coder/nebulachaos/internal/nemesis"
)

var faultColor = map[string]string{
	"partition": "#f97316", "delay": "#eab308", "pause": "#a855f7", "kill": "#ef4444",
	"powerloss": "#dc2626", "flip-log": "#ec4899", "corrupt-snapshot": "#ec4899",
	"snapshot-header": "#ec4899", "delete-snapshot": "#ec4899",
}

func opColor(o history.Op) string {
	switch {
	case o.Status == history.Info:
		return "#f59e0b"
	case o.Status == history.Fail:
		return "#64748b"
	case o.Kind == history.Read:
		return "#38bdf8"
	case o.Kind == history.Write:
		return "#22c55e"
	}
	return "#a78bfa"
}

// WriteTimeline renders a self-contained HTML report of a run.
func WriteTimeline(path string, sum *Summary, ops []history.Op, faults []nemesis.Record, rep checker.Report) error {
	var b strings.Builder
	end := int64(sum.Duration * 1e9)
	for _, o := range ops {
		end = max(end, o.Complete)
	}
	verdict, vclass := "LINEARIZABLE", "ok"
	if !sum.Valid {
		verdict, vclass = "NOT LINEARIZABLE", "bad"
	} else if sum.Unknown {
		verdict, vclass = "UNKNOWN", "warn"
	}
	bad := map[int]bool{}
	for _, k := range rep.Keys {
		for _, o := range k.Minimal {
			bad[o.ID] = true
		}
	}

	fmt.Fprintf(&b, `<!doctype html><html><head><meta charset="utf-8"><title>NebulaChaos · %s</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{--bg:#070912;--panel:#0e1222;--line:#1e2440;--text:#e6e9f7;--dim:#8b93b8;--accent:#a78bfa}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 Inter,system-ui,-apple-system,sans-serif}
header{padding:28px 32px 18px;border-bottom:1px solid var(--line);display:flex;gap:24px;align-items:center;flex-wrap:wrap}
h1{margin:0;font-size:24px;letter-spacing:-.02em}h1 small{color:var(--dim);font-weight:400;font-size:15px;margin-left:8px}
.badge{padding:8px 16px;border-radius:999px;font-weight:800;letter-spacing:.06em;font-size:15px}
.badge.ok{background:rgba(34,197,94,.15);color:#4ade80;border:1px solid #16a34a}
.badge.bad{background:rgba(239,68,68,.15);color:#f87171;border:1px solid #dc2626}
.badge.warn{background:rgba(245,158,11,.15);color:#fbbf24;border:1px solid #d97706}
.stats{display:flex;gap:22px;flex-wrap:wrap;color:var(--dim)}.stats b{color:var(--text);font-size:18px;display:block}
main{padding:20px 32px 40px}section{background:var(--panel);border:1px solid var(--line);border-radius:14px;padding:18px 20px;margin-bottom:18px}
h2{margin:0 0 12px;font-size:16px}.scroll{overflow-x:auto}
.anom{border-color:#dc2626;box-shadow:0 0 40px rgba(220,38,38,.12)}.anom h2{color:#f87171}
pre{background:#05070e;border:1px solid var(--line);border-radius:10px;padding:12px 14px;overflow-x:auto;font:12.5px/1.6 "JetBrains Mono",ui-monospace,Menlo,monospace;color:#cbd5f5}
.legend{display:flex;gap:16px;flex-wrap:wrap;color:var(--dim);font-size:12.5px;margin-bottom:8px}.legend i{display:inline-block;width:12px;height:12px;border-radius:3px;margin-right:6px;vertical-align:-1px}
table{border-collapse:collapse;width:100%%;font-size:13px}td,th{padding:6px 8px;border-bottom:1px solid var(--line);text-align:left}th{color:var(--dim);font-weight:500}
a{color:var(--accent)}
</style></head><body>
<header><div><h1>NebulaChaos<small>%s · target %s%s</small></h1></div><span class="badge %s">%s</span>
<div class="stats"><span><b>%d</b>operations</span><span><b>%d</b>ok</span><span><b>%d</b>indeterminate</span><span><b>%d</b>faults</span><span><b>%.1fs</b>run</span><span><b>%dms</b>check</span></div></header><main>
`, html.EscapeString(sum.Name), html.EscapeString(sum.Name), html.EscapeString(sum.Target), bugsLabel(sum.Bugs), vclass, verdict,
		sum.Ops, sum.OK, sum.Info, sum.Faults, sum.Duration, sum.CheckMillis)

	for _, k := range rep.Keys {
		if len(k.Minimal) == 0 {
			continue
		}
		fmt.Fprintf(&b, `<section class="anom"><h2>⚠ %s on key %s — minimal counterexample (%d of %d operations)</h2>`,
			html.EscapeString(k.Anomaly), html.EscapeString(k.Key), len(k.Minimal), k.Ops)
		b.WriteString(zoomSVG(k.Minimal, faults))
		b.WriteString("<pre>")
		for _, l := range k.Explain {
			b.WriteString(html.EscapeString(l) + "\n")
		}
		b.WriteString("</pre></section>\n")
	}

	b.WriteString(`<section><h2>Timeline</h2><div class="legend"><span><i style="background:#38bdf8"></i>read</span><span><i style="background:#22c55e"></i>write</span><span><i style="background:#a78bfa"></i>cas</span><span><i style="background:#64748b"></i>failed</span><span><i style="background:#f59e0b"></i>indeterminate (timed out)</span><span><i style="background:none;border:2px solid #ef4444"></i>in counterexample</span></div><div class="scroll">`)
	b.WriteString(fullSVG(ops, faults, end, bad))
	b.WriteString("</div></section>\n")

	b.WriteString(`<section><h2>Faults injected</h2><table><tr><th>at</th><th>until</th><th>fault</th><th>what happened</th></tr>`)
	for _, f := range faults {
		until := "final heal"
		if f.End > 0 {
			until = fmt.Sprintf("%.2fs", float64(f.End)/1e9)
		}
		if f.End == f.At {
			until = "—"
		}
		detail := f.Detail
		if f.Error != "" {
			detail += " (" + f.Error + ")"
		}
		fmt.Fprintf(&b, `<tr><td>%.2fs</td><td>%s</td><td style="color:%s">%s</td><td>%s</td></tr>`,
			float64(f.At)/1e9, until, faultColor[f.Do], f.Do, html.EscapeString(detail))
	}
	b.WriteString("</table></section>\n")

	if len(sum.Stalls) > 0 {
		b.WriteString(`<section><h2>Write availability after each fault</h2><table><tr><th>fault</th><th>at</th><th>longest write stall (next 10 s)</th></tr>`)
		for _, s := range sum.Stalls {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>%.2fs</td><td>%.2fs</td></tr>", s.Fault, s.At, s.Stall)
		}
		b.WriteString("</table></section>\n")
	}
	if len(sum.Refused) > 0 {
		b.WriteString(`<section><h2>Nodes that refused to start</h2><pre>`)
		for n, e := range sum.Refused {
			fmt.Fprintf(&b, "%s: %s\n", n, html.EscapeString(e))
		}
		b.WriteString("</pre></section>")
	}
	fmt.Fprintf(&b, `<section><h2>Verdict per key</h2><table><tr><th>key</th><th>ops</th><th>result</th></tr>`)
	for _, k := range rep.Keys {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%d</td><td>%s</td></tr>", k.Key, k.Ops, k.Result)
	}
	fmt.Fprintf(&b, `</table><p style="color:var(--dim)">Own WGL checker and Porcupine (%s) %s.`, rep.Porcupine, agreeWord(rep.Agree))
	if !sum.Valid {
		b.WriteString(` <a href="porcupine.html">Porcupine's interactive view</a>.`)
	}
	b.WriteString("</p></section></main></body></html>\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func bugsLabel(b string) string {
	if b == "" {
		return ""
	}
	return " · planted bug: " + html.EscapeString(b)
}

func agreeWord(ok bool) string {
	if ok {
		return "agree"
	}
	return "DISAGREE"
}

func fullSVG(ops []history.Op, faults []nemesis.Record, end int64, bad map[int]bool) string {
	clients := 0
	for _, o := range ops {
		clients = max(clients, o.Client+1)
	}
	const pxPerSec, rowH, top, left = 70.0, 22, 30, 40
	w := left + int(float64(end)/1e9*pxPerSec) + 20
	h := top + clients*rowH + 30
	x := func(t int64) float64 { return left + float64(t)/1e9*pxPerSec }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" font-family="Inter,system-ui,sans-serif" font-size="11">`, w, h)
	for _, f := range faults {
		e := f.End
		if e == 0 {
			e = end
		}
		if e-f.At < 60e6 {
			e = f.At + 60e6
		}
		c := faultColor[f.Do]
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="%s" opacity=".13"/><text x="%.1f" y="18" fill="%s">%s</text>`,
			x(f.At), top-6, x(e)-x(f.At), clients*rowH+6, c, x(f.At)+3, c, html.EscapeString(f.Do))
	}
	for s := 0; float64(s) <= float64(end)/1e9; s += 5 {
		fmt.Fprintf(&b, `<line x1="%.1f" x2="%.1f" y1="%d" y2="%d" stroke="#1e2440"/><text x="%.1f" y="%d" fill="#8b93b8">%ds</text>`,
			x(int64(s)*1e9), x(int64(s)*1e9), top-6, top+clients*rowH, x(int64(s)*1e9)-6, h-8, s)
	}
	for c := 0; c < clients; c++ {
		fmt.Fprintf(&b, `<text x="4" y="%d" fill="#8b93b8">c%d</text>`, top+c*rowH+15, c)
	}
	sorted := append([]history.Op(nil), ops...)
	sort.Slice(sorted, func(i, j int) bool { return bad[sorted[i].ID] == false && bad[sorted[j].ID] })
	for _, o := range sorted {
		e := o.Complete
		dash := ""
		if o.Status == history.Info {
			dash = ` stroke-dasharray="3 2"`
		}
		wd := math.Max(x(e)-x(o.Invoke), 1.5)
		stroke := ""
		if bad[o.ID] {
			stroke = ` stroke="#ef4444" stroke-width="2.5"`
		} else if dash != "" {
			stroke = ` stroke="#f59e0b"` + dash
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" rx="2" fill="%s" opacity=".85"%s><title>%s via %s, %.3fs–%.3fs</title></rect>`,
			x(o.Invoke), top+o.Client*rowH+3, wd, rowH-8, opColor(o), stroke, html.EscapeString(o.String()), o.Node,
			float64(o.Invoke)/1e9, float64(e)/1e9)
	}
	b.WriteString("</svg>")
	return b.String()
}

// zoomSVG draws just the counterexample, one row per operation, large
// enough to read on a slide.
func zoomSVG(min []history.Op, faults []nemesis.Record) string {
	var lo, hi int64 = math.MaxInt64, 0
	for _, o := range min {
		lo = min64(lo, o.Invoke)
		if o.Status != history.Info {
			hi = max(hi, o.Complete)
		} else {
			hi = max(hi, o.Invoke)
		}
	}
	pad := max((hi-lo)/8, 20e6)
	lo, hi = lo-pad, hi+pad
	const W, rowH, top, left = 1100.0, 44, 26, 330
	x := func(t int64) float64 { return left + float64(t-lo)/float64(hi-lo)*(W-left-20) }
	h := top + len(min)*rowH + 28
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" style="width:100%%;max-width:%dpx" font-family="Inter,system-ui,sans-serif">`, int(W), h, int(W))
	for _, f := range faults {
		e := f.End
		if e == 0 {
			e = hi
		}
		if e < lo || f.At > hi {
			continue
		}
		s, en := max(f.At, lo), min64(e, hi)
		c := faultColor[f.Do]
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="%s" opacity=".12"/><text x="%.1f" y="16" fill="%s" font-size="12">%s</text>`,
			x(s), top-6, x(en)-x(s), len(min)*rowH+6, c, x(s)+4, c, html.EscapeString(f.Detail))
	}
	for i, o := range min {
		y := top + i*rowH
		e := o.Complete
		if o.Status == history.Info {
			e = hi
		}
		fmt.Fprintf(&b, `<text x="8" y="%d" fill="#e6e9f7" font-size="14" font-family="JetBrains Mono,ui-monospace,Menlo,monospace">%s</text>`, y+24, html.EscapeString(o.String()))
		fmt.Fprintf(&b, `<text x="8" y="%d" fill="#8b93b8" font-size="11">p%d via %s</text>`, y+38, o.Process, o.Node)
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="26" rx="5" fill="%s" opacity=".9"/>`, x(o.Invoke), y+8, math.Max(x(e)-x(o.Invoke), 3), opColor(o))
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" fill="#8b93b8" font-size="11">%.3fs</text>`, x(o.Invoke), y+46, float64(o.Invoke)/1e9)
	}
	b.WriteString("</svg>")
	return b.String()
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
