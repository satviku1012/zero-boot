#!/usr/bin/env python3
"""plot_benchmark.py: turn scripts/benchmark.sh results into the README charts.

Usage: python3 scripts/plot_benchmark.py [results.csv] [output dir]
       (defaults: docs/benchmark/results.csv, docs/images)

Writes a light and a dark SVG for each chart; the README picks one with a
<picture> tag. Standard library only, so it runs anywhere python3 does.
"""
import csv
import math
import sys
from pathlib import Path

# Colors: warm = blue, cold = orange, each stepped separately for the light
# and dark card surfaces (checked for colorblind separation and contrast).
THEMES = {
    "light": dict(surface="#fcfcfb", border="#e6e5e1", text="#0b0b0b", muted="#52514e",
                  grid="#ebeae6", axis="#a8a7a1", warm="#2a78d6", cold="#eb6834"),
    "dark": dict(surface="#1a1a19", border="#30302d", text="#f0efec", muted="#c3c2b7",
                 grid="#2b2b28", axis="#5f5e59", warm="#3987e5", cold="#d95926"),
}
FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"
W = 760  # every chart is this wide


def pct(values, p):
    """Nearest-rank percentile, the same definition benchmark.sh prints."""
    s = sorted(values)
    return s[max(0, math.ceil(p / 100 * len(s)) - 1)]


def fmt(ms):
    return f"{ms:.1f} ms" if ms < 100 else f"{ms:.0f} ms"


class SVG:
    """A tiny SVG builder: a card with a title, then primitive shapes."""

    def __init__(self, height, theme, title, subtitle):
        self.t, self.h, self.title, self.parts = theme, height, title, []
        self.parts.append(f'<rect x="0.5" y="0.5" width="{W - 1}" height="{height - 1}" rx="10" '
                          f'fill="{theme["surface"]}" stroke="{theme["border"]}"/>')
        self.text(28, 38, title, size=17, weight=600)
        self.text(28, 62, subtitle, size=13, color="muted")

    def text(self, x, y, s, size=12, color="text", anchor="start", weight=400):
        s = s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        self.parts.append(f'<text x="{x:.1f}" y="{y:.1f}" font-size="{size}" font-weight="{weight}" '
                          f'fill="{self.t.get(color, color)}" text-anchor="{anchor}">{s}</text>')

    def line(self, x1, y1, x2, y2, color="grid", width=1, dash=None):
        d = f' stroke-dasharray="{dash}"' if dash else ""
        self.parts.append(f'<line x1="{x1:.1f}" y1="{y1:.1f}" x2="{x2:.1f}" y2="{y2:.1f}" '
                          f'stroke="{self.t.get(color, color)}" stroke-width="{width}"{d}/>')

    def bar(self, x, y, w, h, color, end="right"):
        """A bar whose free end is rounded (4px) and whose base end is square."""
        r = min(4, w / 2, h / 2)
        c = self.t[color]
        if end == "right":
            d = (f"M{x:.1f},{y:.1f}h{w - r:.1f}a{r},{r} 0 0 1 {r},{r}v{h - 2 * r:.1f}"
                 f"a{r},{r} 0 0 1 {-r},{r}h{-(w - r):.1f}z")
        else:  # rounded top
            d = (f"M{x:.1f},{y + h:.1f}v{-(h - r):.1f}a{r},{r} 0 0 1 {r},{-r}h{w - 2 * r:.1f}"
                 f"a{r},{r} 0 0 1 {r},{r}v{h - r:.1f}z")
        self.parts.append(f'<path d="{d}" fill="{c}"/>')

    def dot(self, x, y, color):
        # A thin surface ring keeps overlapping dots distinguishable.
        self.parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="4" fill="{self.t[color]}" '
                          f'stroke="{self.t["surface"]}" stroke-width="1"/>')

    def swatch(self, x, y, color, label):
        self.parts.append(f'<circle cx="{x + 5}" cy="{y - 4}" r="5" fill="{self.t[color]}"/>')
        self.text(x + 16, y, label, size=13)

    def save(self, path):
        body = "\n  ".join(self.parts)
        path.write_text(
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{self.h}" '
            f'viewBox="0 0 {W} {self.h}" role="img" aria-label="{self.title}" '
            f'font-family="{FONT}">\n  <title>{self.title}</title>\n  {body}\n</svg>\n')


def percentiles_chart(t, warm, cold):
    """Horizontal bars: p50/p95/p99 for warm hits and cold starts, one axis."""
    rows = [("warm", f"Warm hit · n={len(warm)}", warm), ("cold", f"Cold start · n={len(cold)}", cold)]
    x0, x1 = 150, W - 110
    top, bar_h, gap, group_gap = 96, 20, 10, 34
    xmax = math.ceil(pct(cold, 99) / 100) * 100
    sx = lambda v: x0 + (x1 - x0) * v / xmax
    height = top + 2 * (3 * (bar_h + gap)) + group_gap + 56
    s = SVG(height, t, "Allocation latency: warm hit vs cold start",
            "Time for ClaimSandbox to return a running sandbox (lower is better)")
    plot_bottom = height - 50
    for v in range(0, xmax + 1, 100):  # recessive vertical grid
        s.line(sx(v), top - 12, sx(v), plot_bottom, "grid")
        s.text(sx(v), plot_bottom + 18, f"{v} ms", size=11, color="muted", anchor="middle")
    y = top
    for color, heading, values in rows:
        s.swatch(28, y + 2, color, heading)
        y += 14
        for p in (50, 95, 99):
            v = pct(values, p)
            s.text(x0 - 12, y + bar_h / 2 + 4, f"p{p}", size=12, color="muted", anchor="end")
            s.bar(x0, y, max(sx(v) - x0, 2), bar_h, color)
            s.text(max(sx(v), x0 + 2) + 8, y + bar_h / 2 + 4, fmt(v), size=12, weight=600)
            y += bar_h + gap
        y += group_gap - 14
    s.line(x0, top - 12, x0, plot_bottom, "axis")  # baseline the bars grow from
    return s


def histogram_chart(t, warm):
    """Distribution of steady-state warm allocation latency, 0.25ms bins."""
    p50, p99 = pct(warm, 50), pct(warm, 99)
    step = 0.25
    cap = max(4.0, math.ceil(p99 * 1.5))  # everything above lands in one overflow bin
    nbins = int(cap / step)
    counts = [0] * (nbins + 1)
    for v in warm:
        counts[min(int(v / step), nbins)] += 1
    x0, x1, top, bottom = 70, W - 30, 110, 330
    ymax = math.ceil(max(counts) / 50) * 50
    bw = (x1 - x0) / len(counts)
    sx = lambda v: x0 + (x1 - x0) * v / (cap + step)
    sy = lambda c: bottom - (bottom - top) * c / ymax
    s = SVG(bottom + 64, t, f"Warm-hit allocation latency, {len(warm)} requests",
            "Requests paced 150ms apart so the pool stays warm, the steady state")
    for c in range(0, ymax + 1, 50):
        s.line(x0, sy(c), x1, sy(c), "grid")
        s.text(x0 - 10, sy(c) + 4, str(c), size=11, color="muted", anchor="end")
    for i, c in enumerate(counts):
        if c:  # 2px surface gap between adjacent bars
            s.bar(x0 + i * bw + 1, sy(c), bw - 2, bottom - sy(c), "warm", end="top")
    for v in range(0, int(cap)):  # the last tick is replaced by the overflow label
        s.text(sx(v), bottom + 18, f"{v} ms", size=11, color="muted", anchor="middle")
    s.text(x0 + nbins * bw + bw / 2, bottom + 18, f"≥{cap:g} ms", size=11, color="muted", anchor="end")
    s.line(x0, bottom, x1, bottom, "axis")
    for label, v in (("p50", p50), ("p99", p99)):
        x = sx(v)
        s.line(x, top - 14, x, bottom, "text", dash="4 4")
        s.text(x + 6, top - 4, f"{label} {v:.2f} ms", size=12, weight=600)
    s.text(18, top - 22, "requests", size=11, color="muted")
    return s


def burst_chart(t, bursts):
    """Each burst fires 10 requests at a pool of 5: ranks 1-5 warm, 6-10 cold."""
    x0, x1, top, bottom = 80, W - 30, 116, 340
    ymax = math.ceil(max(v for b in bursts for _, v in b) / 100) * 100
    col = (x1 - x0) / 10
    sx = lambda rank: x0 + col * (rank - 0.5)
    sy = lambda v: bottom - (bottom - top) * v / ymax
    s = SVG(bottom + 70, t, f"Bursts of 10 concurrent requests against a pool of 5 ({len(bursts)} bursts)",
            "Each dot is one request, sorted fastest to slowest within its burst")
    for v in range(0, ymax + 1, 100):
        s.line(x0, sy(v), x1, sy(v), "grid")
        s.text(x0 - 10, sy(v) + 4, f"{v} ms", size=11, color="muted", anchor="end")
    s.line(x0, bottom, x1, bottom, "axis")
    for i, b in enumerate(bursts):
        # Spread bursts sideways within their column so overlapping dots stay visible.
        jitter = (i - (len(bursts) - 1) / 2) * col * 0.8 / max(len(bursts), 1)
        for rank, (status, v) in enumerate(sorted(b, key=lambda r: r[1]), start=1):
            s.dot(sx(rank) + jitter, sy(v), "warm" if status == "WARM_HIT" else "cold")
    for rank in range(1, 11):
        s.text(sx(rank), bottom + 18, str(rank), size=11, color="muted", anchor="middle")
    s.text((x0 + x1) / 2, bottom + 40, "request rank within burst", size=12, color="muted", anchor="middle")
    xd = x0 + col * 5
    s.line(xd, top - 10, xd, bottom, "text", dash="4 4")
    s.text(xd - 8, top + 2, "pool of 5 drained", size=12, anchor="end", weight=600)
    s.swatch(x1 - 250, 92, "warm", "Warm hit")
    s.swatch(x1 - 140, 92, "cold", "Cold start")
    return s


def main():
    src = Path(sys.argv[1] if len(sys.argv) > 1 else "docs/benchmark/results.csv")
    out = Path(sys.argv[2] if len(sys.argv) > 2 else "docs/images")
    out.mkdir(parents=True, exist_ok=True)
    rows = list(csv.DictReader(src.open()))
    alloc = lambda r: float(r["alloc_ms"])

    warm = [alloc(r) for r in rows if r["phase"] == "dist" and r["status"] == "WARM_HIT"]
    cold = [alloc(r) for r in rows if r["phase"] in ("burst", "bursts") and r["status"] == "COLD_MISS"]
    burst_rows = [(r["status"], alloc(r)) for r in rows if r["phase"] == "bursts"]
    bursts = [burst_rows[i:i + 10] for i in range(0, len(burst_rows) - 9, 10)]

    for mode, theme in THEMES.items():
        percentiles_chart(theme, warm, cold).save(out / f"latency-percentiles-{mode}.svg")
        histogram_chart(theme, warm).save(out / f"warm-latency-histogram-{mode}.svg")
        burst_chart(theme, bursts).save(out / f"burst-behavior-{mode}.svg")
    print(f"wrote 6 charts to {out}/ (warm n={len(warm)}, cold n={len(cold)}, bursts={len(bursts)})")


if __name__ == "__main__":
    main()
