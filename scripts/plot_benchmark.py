#!/usr/bin/env python3
"""plot_benchmark.py: draw the README charts from the benchmark results.

Usage: python3 scripts/plot_benchmark.py [results dir] [output dir]
       (defaults: docs/benchmark, docs/images)

Reads results.csv (scripts/benchmark.sh) and sweep.csv (scripts/load_sweep.py)
and writes two multi-panel SVG figures. Standard library only.
"""
import csv
import math
import statistics
import sys
from pathlib import Path

FONT = "'Times New Roman', Times, 'STIX Two Text', Georgia, serif"
INK, MUTED, GRID, BG = "#111111", "#555555", "#e6e6e6", "#ffffff"
BLUE, ORANGE = "#3366dd", "#d4782f"
POOL, REFILL_S = 5, 0.48  # pool size, and measured seconds per refill spawn


def pct(values, p):
    """Nearest-rank percentile (the definition benchmark.sh prints)."""
    s = sorted(values)
    return s[max(0, math.ceil(p / 100 * len(s)) - 1)]


def esc(s):
    return str(s).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


class Figure:
    """A white card with a title block, panels, a legend and footnotes."""

    def __init__(self, width, height, title, subtitle):
        self.w, self.h, self.title = width, height, title
        self.parts = [f'<rect width="{width}" height="{height}" fill="{BG}"/>']
        self.text(88, 46, title, 28, weight=700)
        self.text(88, 66, subtitle, 13, MUTED)

    def text(self, x, y, s, size=13, color=INK, anchor="start", weight=400, rotate=None):
        tr = f' transform="rotate({rotate} {x:.1f} {y:.1f})"' if rotate else ""
        self.parts.append(f'<text x="{x:.1f}" y="{y:.1f}" font-size="{size}" font-weight="{weight}" '
                          f'fill="{color}" text-anchor="{anchor}"{tr}>{esc(s)}</text>')

    def raw(self, svg):
        self.parts.append(svg)

    def legend(self, items, x, y):
        """items: (label, color, dashed). Line + marker swatches, stacked."""
        for i, (label, color, dashed) in enumerate(items):
            yy = y + i * 20
            d = ' stroke-dasharray="6 4"' if dashed else ""
            self.raw(f'<line x1="{x}" y1="{yy}" x2="{x + 30}" y2="{yy}" stroke="{color}" stroke-width="2.5"{d}/>')
            if not dashed:
                self.raw(f'<circle cx="{x + 15}" cy="{yy}" r="3.5" fill="{color}"/>')
            self.text(x + 40, yy + 4, label, 13)

    def footnotes(self, lines):
        for i, line in enumerate(lines):
            self.text(88, self.h - 40 + i * 18, line, 11.5, MUTED)

    def save(self, path):
        path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" '
                        f'viewBox="0 0 {self.w} {self.h}" font-family="{FONT}" role="img" '
                        f'aria-label="{esc(self.title)}">\n<title>{esc(self.title)}</title>\n'
                        + "\n".join(self.parts) + "\n</svg>\n")


class Panel:
    """One plot area. Scales: 'lin', 'log2' or 'log10' on either axis."""

    def __init__(self, fig, x, y, w, h, title, xdom, ydom, xscale="lin", yscale="lin"):
        self.f, self.x, self.y, self.w, self.h = fig, x, y, w, h
        self.xdom, self.ydom, self.xs, self.ys = xdom, ydom, xscale, yscale
        fig.text(x, y - 22, title, 16, weight=700)

    @staticmethod
    def _t(v, scale):
        if scale == "log2":
            return math.log2(v)
        return math.log10(v) if scale == "log10" else v

    def px(self, v):
        a, b = (self._t(d, self.xs) for d in self.xdom)
        return self.x + 10 + (self.w - 20) * (self._t(v, self.xs) - a) / (b - a)

    def py(self, v):
        a, b = (self._t(d, self.ys) for d in self.ydom)
        return self.y + self.h - self.h * (self._t(v, self.ys) - a) / (b - a)

    def axes(self, xticks, yticks, xlabel, ylabel=None, xfmt=str, yfmt=str):
        f, bottom = self.f, self.y + self.h
        for v in yticks:  # light horizontal grid + left ticks
            y = self.py(v)
            f.raw(f'<line x1="{self.x}" y1="{y:.1f}" x2="{self.x + self.w}" y2="{y:.1f}" stroke="{GRID}"/>')
            f.raw(f'<line x1="{self.x - 5}" y1="{y:.1f}" x2="{self.x}" y2="{y:.1f}" stroke="{INK}"/>')
            f.text(self.x - 9, y + 4.5, yfmt(v), 13, anchor="end")
        for v in xticks:
            x = self.px(v)
            f.raw(f'<line x1="{x:.1f}" y1="{bottom}" x2="{x:.1f}" y2="{bottom + 5}" stroke="{INK}"/>')
            f.text(x, bottom + 21, xfmt(v), 13, anchor="middle")
        # Left and bottom spines only.
        f.raw(f'<path d="M{self.x},{self.y - 8}V{bottom}H{self.x + self.w}" fill="none" stroke="{INK}"/>')
        f.text(self.x + self.w / 2, bottom + 42, xlabel, 14, anchor="middle")
        if ylabel:
            f.text(self.x - 52, self.y + self.h / 2, ylabel, 14, anchor="middle", rotate=-90)

    def band(self, xs, lo, hi, color):
        pts = [f"{self.px(x):.1f},{self.py(v):.1f}" for x, v in zip(xs, hi)]
        pts += [f"{self.px(x):.1f},{self.py(v):.1f}" for x, v in reversed(list(zip(xs, lo)))]
        self.f.raw(f'<polygon points="{" ".join(pts)}" fill="{color}" fill-opacity="0.16"/>')

    def line(self, xs, ys, color, markers=True, dashed=False, width=2.5):
        pts = " ".join(f"{self.px(x):.1f},{self.py(y):.1f}" for x, y in zip(xs, ys))
        d = ' stroke-dasharray="6 4"' if dashed else ""
        self.f.raw(f'<polyline points="{pts}" fill="none" stroke="{color}" stroke-width="{width}" '
                   f'stroke-linejoin="round"{d}/>')
        if markers:
            for x, y in zip(xs, ys):
                self.f.raw(f'<circle cx="{self.px(x):.1f}" cy="{self.py(y):.1f}" r="3.5" fill="{color}"/>')


def sweep_figure(rows):
    """Warm-hit rate, p50 and p99 allocation latency vs offered load."""
    rates = sorted({float(r["rate"]) for r in rows})
    trials = sorted({r["trial"] for r in rows})
    metrics = {"warm": [], "p50": [], "p99": []}  # per rate: list of per-trial values
    for rate in rates:
        per = {"warm": [], "p50": [], "p99": []}
        for t in trials:
            tr = [r for r in rows if float(r["rate"]) == rate and r["trial"] == t and r["status"] != "ERROR"]
            alloc = [float(r["alloc_ms"]) for r in tr]
            per["warm"].append(100 * sum(r["status"] == "WARM_HIT" for r in tr) / len(tr))
            per["p50"].append(pct(alloc, 50))
            per["p99"].append(pct(alloc, 99))
        for k in metrics:
            metrics[k].append(per[k])
    errors = sum(r["status"] == "ERROR" for r in rows)

    fig = Figure(1240, 560, "Warm pool under load",
                 f"2 worker nodes · pool of {POOL} · ~480 ms cold start · open-loop requests · localhost")
    fig.legend([(f"Measured (pool of {POOL})", BLUE, False),
                (f"Refill model: {POOL} ÷ {REFILL_S} s ≈ {POOL / REFILL_S:.0f} warm/s", ORANGE, True)], 870, 40)
    xt = [2, 4, 8, 16, 32]
    ymax = math.ceil(max(max(v) for v in metrics["p99"]) / 100) * 100
    specs = [("Warm-hit rate", "warm", (0, 100), range(0, 101, 20), "Warm hits (%)"),
             ("p50 allocation latency (ms)", "p50", (0, ymax), range(0, ymax + 1, 100), None),
             ("p99 allocation latency (ms)", "p99", (0, ymax), range(0, ymax + 1, 100), None)]
    for i, (title, key, ydom, yticks, ylabel) in enumerate(specs):
        p = Panel(fig, 108 + i * 380, 130, 320, 280, title, (rates[0], rates[-1]), ydom, "log2")
        p.axes(xt, yticks, "Offered load (requests/s)", ylabel)
        vals = metrics[key]
        p.band(rates, [min(v) for v in vals], [max(v) for v in vals], BLUE)
        if key == "warm":  # what the refill rate alone predicts
            xs = [rates[0] * (rates[-1] / rates[0]) ** (j / 60) for j in range(61)]
            p.line(xs, [min(100, 100 * POOL / REFILL_S / x) for x in xs], ORANGE, markers=False, dashed=True)
        p.line(rates, [statistics.median(v) for v in vals], BLUE)
    fig.footnotes([f"{len(trials)} trials × 10 s per point · median line, min–max band · "
                   f"{len(rows):,} requests · {errors} errors",
                   "Apple M3 Pro · Docker Desktop (Linux 6.12, cgroup v2) · requests start on a fixed "
                   "schedule (open loop); 4 s refill pause between trials."])
    return fig


def latency_figure(rows):
    """CDF of warm vs cold allocation latency, and latency by rank in a burst."""
    warm = sorted(float(r["alloc_ms"]) for r in rows if r["phase"] == "dist" and r["status"] == "WARM_HIT")
    cold = sorted(float(r["alloc_ms"]) for r in rows if r["status"] == "COLD_MISS")
    burst_rows = [float(r["alloc_ms"]) for r in rows if r["phase"] == "bursts"]
    bursts = [sorted(burst_rows[i:i + 10]) for i in range(0, len(burst_rows) - 9, 10)]

    fig = Figure(1240, 560, "Allocation latency",
                 "Time for ClaimSandbox to return a running sandbox · 2 worker nodes · pool of 5 · localhost")
    fig.legend([(f"Warm hit (n = {len(warm):,})", BLUE, False),
                (f"Cold start (n = {len(cold)})", ORANGE, False)], 920, 40)
    log_fmt = lambda v: f"{v:,g}"

    # Left: cumulative distribution on a log axis, so 2 ms and 480 ms both read.
    p = Panel(fig, 108, 130, 520, 280, "Cumulative distribution", (0.5, 1000), (0, 100), "log10")
    p.axes([1, 10, 100, 1000], range(0, 101, 20), "Allocation latency (ms, log scale)",
           "Requests at or below (%)", xfmt=log_fmt)
    for data, color in ((warm, BLUE), (cold, ORANGE)):
        xs, ys = [data[0]], [0]
        for i, v in enumerate(data, 1):
            xs.append(v)
            ys.append(100 * i / len(data))
        p.line(xs, ys, color, markers=False)
    fig.text(p.px(pct(warm, 50)) + 8, p.py(50), f"p50 {pct(warm, 50):.2f} ms", 12, MUTED)
    fig.text(p.px(pct(warm, 99)) + 8, p.py(90), f"p99 {pct(warm, 99):.2f} ms", 12, MUTED)
    fig.text(p.px(pct(cold, 50)) - 8, p.py(50), f"p50 {pct(cold, 50):.0f} ms", 12, MUTED, anchor="end")

    # Right: 10 concurrent requests against a pool of 5, by rank within the burst.
    q = Panel(fig, 738, 130, 440, 280, f"Bursts of 10 concurrent requests ({len(bursts)} bursts)",
              (1, 10), (0.5, 1000), "lin", "log10")
    q.axes(range(1, 11), [1, 10, 100, 1000], "Request rank within burst (fastest → slowest)",
           "Allocation latency (ms, log scale)", yfmt=log_fmt)
    for ranks, color in ((range(1, 6), BLUE), (range(6, 11), ORANGE)):
        cols = [[b[r - 1] for b in bursts] for r in ranks]
        q.band(list(ranks), [min(c) for c in cols], [max(c) for c in cols], color)
        q.line(list(ranks), [statistics.median(c) for c in cols], color)
    xd = (q.px(5) + q.px(6)) / 2
    fig.raw(f'<line x1="{xd:.1f}" y1="{q.y}" x2="{xd:.1f}" y2="{q.y + q.h}" stroke="{MUTED}" stroke-dasharray="3 4"/>')
    fig.text(xd + 6, q.py(100) + 30, "pool of 5 drained", 12, MUTED)

    fig.footnotes([f"Warm: {len(warm):,} sequential requests 150 ms apart · cold: requests that found the pool "
                   "empty · bursts: median line, min–max band · 0 request errors",
                   "Apple M3 Pro · Docker Desktop (Linux 6.12, cgroup v2) · client and cluster on the "
                   "same machine; network latency between real hosts is not measured."])
    return fig


def main():
    src = Path(sys.argv[1] if len(sys.argv) > 1 else "docs/benchmark")
    out = Path(sys.argv[2] if len(sys.argv) > 2 else "docs/images")
    out.mkdir(parents=True, exist_ok=True)
    latency_figure(list(csv.DictReader((src / "results.csv").open()))).save(out / "allocation-latency.svg")
    sweep_figure(list(csv.DictReader((src / "sweep.csv").open()))).save(out / "load-sweep.svg")
    print(f"wrote {out}/allocation-latency.svg and {out}/load-sweep.svg")


if __name__ == "__main__":
    main()
