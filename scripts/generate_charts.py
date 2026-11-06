#!/usr/bin/env python3
"""Publication-quality benchmark figures for the Boogle Engine Stage 2 report.

This script replaces the native LaTeX (PGFPlots/TikZ) data plots of report
Section 7 with five curated, vector figures, one per question of the
assignment's benchmarking rubric:

    fig1_query_latency        Query latency vs client concurrency (1-100 VUs),
                              with operational-law bounds
    fig2_indexing_throughput  Indexing throughput (books/s) and CPU cost per book
    fig3_resource_utilisation CPU and memory of MongoDB, Search and nginx
                              (docker stats) plus the CPU cost of one query
    fig4_replication          1 vs 2 Search replicas behind nginx:
                              throughput, p95 latency and relative change
    fig5_threading            Threads and workers: CPU-bound, I/O-bound and
                              datamart-bound speed-ups

Every value is computed from the raw evidence committed under docs/evidence/
(k6 CSV + JSON summaries, docker-stats samples, benchstat CSVs). The k6 files are
byte-identical copies of test/load/results/; docs/evidence/ is read because only
there are the runs labelled by replica count and accompanied by their resource
samples. Each figure is written twice into docs/figures/:

    <name>.pdf   vector, fonts embedded as TrueType (for \\includegraphics)
    <name>.png   300 dpi raster (README, slides)

plus docs/figures/chart_data.csv, the tidy table of every plotted value and every
derived metric quoted in the report (the "table view" of the figures).

Figures are drawn at their final printed width (the report's \\textwidth is
162 mm on A4 with 24 mm margins), so the 7-8.5 pt type in the figures is the
type the reader sees: include them with width=\\textwidth (or the fraction in
WIDTHS below) and never rescale them by a different factor.

Usage, from the repository root:

    python3 scripts/generate_charts.py
    python3 scripts/generate_charts.py --evidence docs/evidence --out docs/figures
    python3 scripts/generate_charts.py --only latency replication

Requires pandas, numpy, matplotlib >= 3.7 and seaborn >= 0.12. The figures use
Lato when it is installed and fall back to Source Sans, Helvetica, Arial,
Liberation Sans or DejaVu Sans, in that order.

Palette (validated for colour-vision deficiency with the OKLab/Machado checks:
lightness band, chroma floor, adjacent and all-pairs CVD separation, contrast
against the white page). Colour follows the entity in every figure:

    MongoDB (mongod)   coral   #E0573F      Search Service   deep teal #009488
    Indexing Service   indigo  #5A55CC      nginx            blue      #3A7BD5
    Ingestion Service  plum    #A23E8C      references       slate inks
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path

import matplotlib

matplotlib.use("Agg")

import matplotlib.pyplot as plt  # noqa: E402
import numpy as np  # noqa: E402
import pandas as pd  # noqa: E402
import seaborn as sns  # noqa: E402
from matplotlib import ticker  # noqa: E402
from matplotlib.lines import Line2D  # noqa: E402
from matplotlib.patches import Patch  # noqa: E402

REPO = Path(__file__).resolve().parents[1]

# --------------------------------------------------------------------------------------
# Design tokens
# --------------------------------------------------------------------------------------
TEXTWIDTH_IN = 162 / 25.4  # \textwidth of report/final_report.tex (A4, 24 mm margins)

INK = "#1E293B"  # primary text (slate 800)
INK_2 = "#475569"  # secondary text, reference lines (slate 600)
INK_3 = "#7C8798"  # tertiary text, de-emphasised series
GRID = "#DCE1E7"  # dashed hairline grid
AXIS = "#B7C0CC"  # baseline
PHASE = "#F1F4F7"  # hold-phase shading
SURFACE = "#FFFFFF"

ENTITY = {
    "mongo": "#E0573F",  # coral: the datamart, the bottleneck in every figure
    "search": "#009488",  # deep teal
    "indexing": "#5A55CC",  # indigo
    "nginx": "#3A7BD5",  # blue
    "ingestion": "#A23E8C",  # plum
}
ENTITY_LABEL = {
    "mongo": "mongod (MongoDB)",
    "search": "Search Service",
    "indexing": "Indexing Service",
    "nginx": "nginx",
    "ingestion": "Ingestion Service",
}
# Ordinal teal ramp (validated light -> dark): 1 replica / p95 / 2 replicas, band edges
TEAL_LIGHT, TEAL, TEAL_DARK = "#52B3A9", "#009488", "#005C54"

LW = 1.8  # data line width (pt)
MS = 5.0  # marker size (pt)
RING = 1.1  # white ring around markers (pt)

FONT_STACK = [
    "Lato",
    "Source Sans 3",
    "Source Sans Pro",
    "Helvetica Neue",
    "Helvetica",
    "Arial",
    "Liberation Sans",
    "DejaVu Sans",
]

RECORDS: list[dict] = []  # every plotted / derived value, written to chart_data.csv


def record(figure: str, panel: str, series: str, x, y, unit: str, note: str = "") -> None:
    RECORDS.append(
        {"figure": figure, "panel": panel, "series": series, "x": x, "y": y, "unit": unit, "note": note}
    )


def apply_style() -> None:
    sns.set_theme(context="paper", style="white", palette=list(ENTITY.values()))
    plt.rcParams.update(
        {
            "font.family": "sans-serif",
            "font.sans-serif": FONT_STACK,
            "font.size": 7.5,
            "axes.titlesize": 8.5,
            "axes.labelsize": 7.5,
            "axes.labelcolor": INK_2,
            "axes.edgecolor": AXIS,
            "axes.linewidth": 0.8,
            "axes.titleweight": "semibold",
            "axes.titlecolor": INK,
            "axes.facecolor": SURFACE,
            "figure.facecolor": SURFACE,
            "savefig.facecolor": SURFACE,
            "xtick.color": INK_2,
            "ytick.color": INK_2,
            "xtick.labelsize": 7,
            "ytick.labelsize": 7,
            "xtick.major.size": 0,
            "ytick.major.size": 0,
            "xtick.major.pad": 4,
            "ytick.major.pad": 3,
            "legend.fontsize": 7,
            "legend.frameon": False,
            "legend.handlelength": 1.8,
            "legend.handletextpad": 0.5,
            "legend.borderaxespad": 0.2,
            "legend.labelcolor": INK,
            "lines.solid_capstyle": "round",
            "lines.solid_joinstyle": "round",
            "pdf.fonttype": 42,  # embed TrueType: crisp, searchable, editable text
            "ps.fonttype": 42,
            "svg.fonttype": "none",
            "axes.unicode_minus": True,
        }
    )


def tidy_axes(ax: plt.Axes, grid: str = "y") -> None:
    """Remove chart junk: no frame except the baseline, light dashed grid behind data."""
    sns.despine(ax=ax, left=True, top=True, right=True, bottom=False)
    ax.set_axisbelow(True)
    if grid:
        ax.grid(axis=grid, color=GRID, linewidth=0.6, linestyle=(0, (2.5, 2.5)))
    ax.tick_params(axis="both", which="both", length=0)


def header(ax: plt.Axes, letter: str, title: str, units: str | None = None) -> None:
    """Panel header in the journal style: bold letter, title, then the y units."""
    ax.set_title(f"{letter}   {title}", loc="left", pad=15 if units else 6, x=-0.0)
    if units:
        ax.annotate(
            units,
            xy=(0, 1),
            xycoords="axes fraction",
            xytext=(0, 4),
            textcoords="offset points",
            ha="left",
            va="bottom",
            fontsize=7,
            color=INK_2,
        )


def reference(ax: plt.Axes, x, y, **kw) -> None:
    """A model or bound: secondary ink, long dashes (distinct from the grid)."""
    ax.plot(x, y, color=INK_2, linewidth=0.9, linestyle=(0, (4.5, 2.5)), zorder=2, **kw)


def threshold(ax: plt.Axes, y: float, **kw) -> None:
    """An objective or a hard capacity: primary ink, fine dots."""
    ax.axhline(y, color=INK, linewidth=0.9, linestyle=(0, (1, 1.6)), zorder=2, **kw)


def note(ax: plt.Axes, x, y, text: str, **kw) -> plt.Text:
    kw.setdefault("fontsize", 6.8)
    kw.setdefault("color", INK_2)
    return ax.text(x, y, text, **kw)


def line(ax, x, y, color, marker="o", lw=LW, ms=MS, face=None, zorder=4, **kw):
    return ax.plot(
        x,
        y,
        color=color,
        linewidth=lw,
        marker=marker,
        markersize=ms,
        markerfacecolor=face or color,
        markeredgecolor=SURFACE if face is None else color,
        markeredgewidth=RING if face is None else 1.0,
        zorder=zorder,
        **kw,
    )


def thousands(ax: plt.Axes, axis: str = "y") -> None:
    fmt = ticker.FuncFormatter(lambda v, _: f"{v:,.0f}")
    (ax.yaxis if axis == "y" else ax.xaxis).set_major_formatter(fmt)


def save(fig: plt.Figure, out: Path, name: str) -> None:
    out.mkdir(parents=True, exist_ok=True)
    fig.savefig(out / f"{name}.pdf")  # exact size: no bbox cropping, layout is constrained
    fig.savefig(out / f"{name}.png", dpi=300)
    plt.close(fig)
    print(f"  wrote {out / name}.pdf / .png")


def new_figure(height_in: float, width_frac: float = 1.0) -> plt.Figure:
    fig = plt.figure(figsize=(TEXTWIDTH_IN * width_frac, height_in), layout="constrained")
    fig.get_layout_engine().set(w_pad=0.04, h_pad=0.04, wspace=0.06, hspace=0.06)
    return fig


# --------------------------------------------------------------------------------------
# Evidence loading
# --------------------------------------------------------------------------------------
def find_one(folder: Path, pattern: str) -> Path:
    matches = sorted(folder.glob(pattern))
    if not matches:
        raise FileNotFoundError(f"no file matches {folder / pattern}")
    return matches[-1]  # timestamped names: the last one is the most recent run


def seconds(duration: str) -> float:
    total, found = 0.0, False
    for value, unit in re.findall(r"([\d.]+)(ms|h|m|s)", duration):
        total += float(value) * {"ms": 1e-3, "s": 1, "m": 60, "h": 3600}[unit]
        found = True
    if not found:
        raise ValueError(f"unparsable duration {duration!r}")
    return total


def mebibytes(raw: str) -> float:
    match = re.fullmatch(r"\s*([\d.]+)\s*([KMGT]?i?)B\s*", raw)
    if not match:
        raise ValueError(f"unparsable memory value {raw!r}")
    scale = {"": 2**-20, "K": 2**-10, "Ki": 2**-10, "M": 1, "Mi": 1, "G": 2**10, "Gi": 2**10, "T": 2**20, "Ti": 2**20}
    return float(match.group(1)) * scale[match.group(2)]


@dataclass
class K6Run:
    """One stepped k6 run: per-level steady-state table + the timing of its hold phases."""

    name: str
    levels: pd.DataFrame
    started: pd.Timestamp
    windows: list[tuple[int, float, float]]  # (VUs, hold start s, hold end s) since start
    ramp_s: float

    @property
    def end_s(self) -> float:
        return self.windows[-1][2] + self.ramp_s  # final ramp-down


def load_k6(csv_path: Path) -> K6Run:
    summary = json.loads(csv_path.with_suffix(".json").read_text(encoding="utf-8"))
    profile = summary["profile"]
    ramp, hold = seconds(profile["ramp_duration"]), seconds(profile["hold_duration"])
    windows = []
    for i, vus in enumerate(profile["vu_levels"]):
        start = i * (ramp + hold) + ramp
        windows.append((int(vus), start, start + hold))
    levels = pd.read_csv(csv_path)
    # The scenario starts when setup() returns; setup() stamps startedAt just before.
    started = pd.Timestamp(summary["k6"]["setup_data"]["startedAt"])
    return K6Run(csv_path.stem, levels, started, windows, ramp)


def load_docker_stats(path: Path, run: K6Run) -> pd.DataFrame:
    """docker-stats samples -> one row per (sample, service), replicas summed."""
    raw = pd.read_csv(path)
    raw["t"] = (pd.to_datetime(raw["timestamp_utc"], utc=True) - run.started).dt.total_seconds()
    # Keep only this run: the sampler of one run may keep recording into the next one.
    raw = raw[(raw["t"] >= -6) & (raw["t"] <= run.end_s + 4)].copy()
    parts = raw["container"].str.extract(r"(search|nginx|mongo|indexing|ingestion)-(\d+)$")
    raw["service"], raw["replica"] = parts[0], parts[1].astype(int)
    raw["cores"] = raw["cpu_percent"] / 100.0  # docker reports 100 % per logical core
    raw["mem_mib"] = raw["mem_used"].map(mebibytes)
    per_service = (
        raw.groupby(["timestamp_utc", "t", "service"], as_index=False)
        .agg(cores=("cores", "sum"), mem_mib=("mem_mib", "sum"), replicas=("replica", "nunique"))
        .sort_values("t")
    )
    return per_service


def per_level(stats: pd.DataFrame, run: K6Run, settle_s: float = 2.0) -> pd.DataFrame:
    """Mean CPU/memory of each service inside each hold window (after `settle_s`)."""
    rows = []
    for vus, start, end in run.windows:
        window = stats[(stats["t"] >= start + settle_s) & (stats["t"] <= end)]
        for service, g in window.groupby("service"):
            rows.append(
                {
                    "vus": vus,
                    "service": service,
                    "cores": g["cores"].mean(),
                    "cores_sd": g["cores"].std(ddof=1),
                    "mem_mib": g["mem_mib"].mean(),
                    "mem_max": g["mem_mib"].max(),
                    "samples": len(g),
                }
            )
    return pd.DataFrame(rows)


def load_benchstat(path: Path) -> pd.DataFrame:
    """sec/op block of a `benchstat -format csv` file -> benchmark, threads, sec, ci (fraction)."""
    rows, metric = [], None
    for raw in path.read_text(encoding="utf-8").splitlines():
        if not raw.strip():
            metric = None
            continue
        cells = raw.split(",")
        if raw.startswith(","):
            metric = cells[1] if len(cells) > 1 and cells[1] else metric
            continue
        if metric != "sec/op" or cells[0] in {"geomean"} or ":" in cells[0]:
            continue
        name = cells[0]
        head, _, suffix = name.rpartition("-")
        threads = int(suffix) if suffix.isdigit() else 1
        base = head if suffix.isdigit() else name
        ci = float(cells[2].rstrip("%")) / 100 if len(cells) > 2 and cells[2] else np.nan
        rows.append({"benchmark": base, "threads": threads, "sec": float(cells[1]), "ci": ci})
    return pd.DataFrame(rows)


def load_ingestion(path: Path) -> pd.DataFrame:
    # Exported by PowerShell: UTF-8 BOM, quoted fields, decimal comma.
    return pd.read_csv(path, encoding="utf-8-sig", decimal=",")


# --------------------------------------------------------------------------------------
# Figure 1 - query latency vs concurrency
# --------------------------------------------------------------------------------------
def fig_query_latency(run: K6Run, res: pd.DataFrame, out: Path) -> None:
    fid = "fig1_query_latency"
    lv = run.levels
    n, x = lv["vus"].to_numpy(float), lv["throughput_rps"].to_numpy(float)

    # Operational laws. D: time per request with no queueing (N/X at one client).
    demand_s = n[0] / x[0]
    sat = res[res["vus"] == n.max()]
    cpu_per_query = sat["cores"].sum() / x[-1]  # core-seconds of all containers per query
    x_bound = 12.0 / cpu_per_query  # utilisation law: 12 logical CPUs / CPU per query
    knee = x_bound * demand_s  # N* = X_max * D
    record(fid, "derived", "demand_D", 1, demand_s * 1e3, "ms", "N/X at 1 VU")
    record(fid, "derived", "server_cpu_per_query_at_100", 100, cpu_per_query * 1e3, "core-ms")
    record(fid, "derived", "throughput_bound", np.nan, x_bound, "req/s", "12 cores / CPU per query")
    record(fid, "derived", "knee_N_star", np.nan, knee, "VUs", "X_bound * D")

    fig = new_figure(2.55)
    gs = fig.add_gridspec(1, 2, width_ratios=[1, 1.12])

    # (a) throughput with asymptotic bounds ------------------------------------------
    ax = fig.add_subplot(gs[0])
    tidy_axes(ax)
    header(ax, "a", "Throughput saturates at ~6 clients", "requests per second")
    nn = np.linspace(0, 320 * demand_s, 50)
    reference(ax, nn, nn / demand_s)
    reference(ax, [knee, 108], [x_bound, x_bound])
    line(ax, n, x, TEAL)
    for vi, xi in zip(n, x):
        record(fid, "a", "throughput", vi, xi, "req/s")
    ax.annotate(
        f"knee  N* = X·D ≈ {knee:.1f}",
        xy=(knee, x_bound),
        xytext=(17, 292),
        fontsize=6.8,
        color=INK,
        arrowprops={"arrowstyle": "-", "color": INK_2, "lw": 0.6, "shrinkA": 1, "shrinkB": 2},
        va="center",
    )
    note(ax, 7.3, 40, f"no queueing:\nX = N / D,  D = {demand_s * 1e3:.1f} ms", va="bottom")
    note(
        ax,
        106,
        x_bound + 7,
        f"CPU bound  {x_bound:.0f} req/s",
        ha="right",
        va="bottom",
    )
    note(ax, 100, x[-1] - 12, f"{x[-1]:.0f} req/s", ha="right", va="top", color=INK, fontsize=7)
    ax.set_xlim(-3, 108)
    ax.set_ylim(0, 320)
    ax.set_xticks([1, 10, 50, 100])
    ax.set_yticks([0, 100, 200, 300])
    ax.set_xlabel("Concurrent clients (k6 VUs, closed model, think time 0)")

    # (b) latency distribution with the asymptotic lower bound -----------------------
    ax = fig.add_subplot(gs[1])
    tidy_axes(ax)
    header(ax, "b", "Latency grows linearly once saturated", "response time, milliseconds")
    ax.fill_between(n, lv["med_ms"], lv["p99_ms"], color=TEAL, alpha=0.13, linewidth=0, zorder=1)
    for col in ("med_ms", "p99_ms"):
        ax.plot(n, lv[col], color=TEAL_LIGHT, linewidth=0.8, zorder=2)
    nn = np.linspace(0, 104, 200)
    reference(ax, nn, np.maximum(demand_s, nn / x_bound) * 1e3)
    threshold(ax, 500)
    line(ax, n, lv["p95_ms"], TEAL)
    line(ax, n, lv["avg_ms"], TEAL_DARK, marker="s", lw=1.2, ms=4.2)
    for col, label in (("med_ms", "p50"), ("avg_ms", "mean"), ("p95_ms", "p95"), ("p99_ms", "p99")):
        for vi, yi in zip(n, lv[col]):
            record(fid, "b", label, vi, yi, "ms")
    for vi in n:
        record(fid, "b", "lower_bound", vi, max(demand_s, vi / x_bound) * 1e3, "ms", "max(D, N/X_bound)")
    end = lv.iloc[-1]
    note(ax, 102.5, end["p99_ms"], f"p99  {end['p99_ms']:.0f}", va="center", color=INK)
    note(ax, 102.5, end["p95_ms"], f"p95  {end['p95_ms']:.0f}", va="center", color=INK)
    note(ax, 102.5, end["avg_ms"] - 30, f"mean  {end['avg_ms']:.0f}", va="center", color=INK)
    note(ax, 3, 512, "p95 objective  500 ms", va="bottom", color=INK)
    handles = [
        Patch(facecolor=TEAL, alpha=0.18, edgecolor=TEAL_LIGHT, linewidth=0.8, label="p50–p99 band"),
        Line2D([], [], color=TEAL, lw=LW, marker="o", ms=MS, mec=SURFACE, mew=RING, label="p95"),
        Line2D([], [], color=TEAL_DARK, lw=1.2, marker="s", ms=4.2, mec=SURFACE, mew=RING, label="mean"),
        Line2D([], [], color=INK_2, lw=0.9, ls=(0, (4.5, 2.5)), label="bound  max(D, N/X)"),
    ]
    ax.legend(handles=handles, loc="upper left", bbox_to_anchor=(0.0, 1.03), ncol=1, handlelength=2.4)
    ax.set_xlim(-3, 128)
    ax.set_ylim(0, 900)
    ax.set_xticks([1, 10, 50, 100])
    ax.set_yticks([0, 200, 400, 600, 800])
    ax.set_xlabel("Concurrent clients (k6 VUs)")
    save(fig, out, fid)


# --------------------------------------------------------------------------------------
# Figure 2 - indexing throughput
# --------------------------------------------------------------------------------------
def fig_indexing(run: K6Run, res: pd.DataFrame, out: Path) -> None:
    fid = "fig2_indexing_throughput"
    lv = run.levels
    n, x = lv["vus"].to_numpy(float), lv["books_per_second"].to_numpy(float)
    eff = x / (x[0] * n)

    fig = new_figure(2.45)
    gs = fig.add_gridspec(1, 2, width_ratios=[1.12, 1])

    # (a) throughput vs concurrent updates -------------------------------------------
    ax = fig.add_subplot(gs[0])
    tidy_axes(ax)
    header(ax, "a", "Indexing throughput scales sub-linearly", "books indexed per second")
    nn = np.array([0, 11.0])
    reference(ax, nn, x[0] * nn)
    ax.fill_between(n, x, x[0] * n, color=ENTITY["mongo"], alpha=0.09, linewidth=0, zorder=1)
    line(ax, n, x, ENTITY["indexing"])
    for vi, xi, ei in zip(n, x, eff):
        record(fid, "a", "books_per_second", vi, xi, "books/s")
        record(fid, "a", "parallel_efficiency", vi, ei * 100, "%")
    for vi, xi, ei in zip(n[1:], x[1:], eff[1:]):
        note(ax, vi + 0.25, xi - 0.35, f"{ei:.0%} efficiency".replace("%", " %"), va="top", ha="left")
    note(ax, 10.4, x[-1] + 0.05, f"{x[-1]:.2f} books/s", va="center", ha="left", color=INK, fontsize=7)
    note(ax, 9.9, 11.6, "linear scaling of 1 client", va="center", ha="right")
    note(ax, 8.0, 6.4, "lost to write\ncontention", ha="center", va="center", color=INK_2, style="italic")
    note(ax, 1.25, x[0] + 0.25, f"{x[0]:.2f}", va="bottom", ha="right", color=INK, fontsize=7)
    ax.set_xlim(0, 13.2)
    ax.set_ylim(0, 12)
    ax.set_xticks([1, 5, 10])
    ax.set_yticks([0, 3, 6, 9, 12])
    ax.set_xlabel("Concurrent POST /index/update clients (k6 VUs)")

    # (b) CPU per indexed book, stacked --------------------------------------------
    ax = fig.add_subplot(gs[1])
    tidy_axes(ax)
    header(ax, "b", "MongoDB pays for the concurrency", "server CPU per indexed book, core-seconds")
    pos = np.arange(len(n))
    mongo = np.array([res[(res.vus == v) & (res.service == "mongo")]["cores"].iloc[0] for v in n]) / x
    index = np.array([res[(res.vus == v) & (res.service == "indexing")]["cores"].iloc[0] for v in n]) / x
    width = 0.42
    ax.bar(pos, mongo, width, color=ENTITY["mongo"], edgecolor=SURFACE, linewidth=1.4, zorder=3)
    ax.bar(pos, index, width, bottom=mongo, color=ENTITY["indexing"], edgecolor=SURFACE, linewidth=1.4, zorder=3)
    for k, (p, m, i, vi) in enumerate(zip(pos, mongo, index, n)):
        inside = f"{m:.2f}" if k == 0 else f"{m:.2f}\n+{m / mongo[0] - 1:.0%}".replace("%", " %")
        ax.text(p, m / 2, inside, ha="center", va="center", color="white", fontsize=7, weight="semibold",
                linespacing=1.25)
        ax.text(p, m + i + 0.04, f"{m + i:.2f}", ha="center", va="bottom", color=INK, fontsize=7)
        record(fid, "b", "mongod_core_s_per_book", vi, m, "core-s")
        record(fid, "b", "indexing_core_s_per_book", vi, i, "core-s")
    growth = mongo[-1] / mongo[0] - 1
    record(fid, "derived", "mongod_cost_growth_1_to_10", np.nan, growth * 100, "%")
    handles = [Patch(color=ENTITY["mongo"], label="mongod"), Patch(color=ENTITY["indexing"], label="Indexing Service")]
    ax.legend(handles=handles, loc="upper left", bbox_to_anchor=(0, 1.0), handlelength=1.0)
    ax.set_xticks(pos, [f"{int(v)}" for v in n])
    ax.set_xlim(-0.6, len(n) - 0.4)
    ax.set_ylim(0, 1.8)
    ax.set_yticks([0, 0.5, 1.0, 1.5])
    ax.set_xlabel("Concurrent clients (k6 VUs)")
    save(fig, out, fid)


# --------------------------------------------------------------------------------------
# Figure 3 - CPU and memory per process
# --------------------------------------------------------------------------------------
def fig_resources(run: K6Run, stats: pd.DataFrame, res: pd.DataFrame, out: Path) -> None:
    fid = "fig3_resource_utilisation"
    order = ["mongo", "search", "nginx"]
    palette = {k: ENTITY[k] for k in order}
    stats = stats[stats["service"].isin(order)]

    fig = new_figure(3.75)
    gs = fig.add_gridspec(2, 2, width_ratios=[2.05, 1], height_ratios=[1.15, 1])
    ax_cpu = fig.add_subplot(gs[0, 0])
    ax_mem = fig.add_subplot(gs[1, 0], sharex=ax_cpu)
    ax_q = fig.add_subplot(gs[:, 1])

    for ax in (ax_cpu, ax_mem):
        tidy_axes(ax)
        for vus, start, end in run.windows:
            ax.axvspan(start, end, color=PHASE, zorder=0, linewidth=0)

    # (a) CPU over time
    header(ax_cpu, "a", "CPU per container, 1 Search replica", "logical cores (host: 12)")
    mongo = stats[stats.service == "mongo"]
    ax_cpu.fill_between(mongo["t"], 0, mongo["cores"], color=ENTITY["mongo"], alpha=0.10, linewidth=0, zorder=1)
    sns.lineplot(
        data=stats, x="t", y="cores", hue="service", hue_order=order, palette=palette,
        estimator=None, sort=True, linewidth=1.5, legend=False, ax=ax_cpu, zorder=3,
    )
    threshold(ax_cpu, 12.0)
    note(ax_cpu, -4, 11.7, "host capacity: 12 logical CPUs", va="top", color=INK)
    for vus, start, end in run.windows:
        note(ax_cpu, (start + end) / 2, 13.3, f"{vus} VU" + ("s" if vus > 1 else ""), ha="center", va="bottom",
             color=INK_2, fontsize=6.8)
    small = stats[stats.service.isin(["search", "nginx"])].groupby("t")["cores"].sum().max()
    ax_cpu.annotate(
        f"Search + nginx  ≤ {small:.2f} cores",
        xy=(124, 0.35), xytext=(118, 3.3), fontsize=6.8, color=INK, ha="center",
        arrowprops={"arrowstyle": "-", "color": INK_2, "lw": 0.6, "shrinkA": 1, "shrinkB": 1},
    )
    ax_cpu.set_ylim(0, 14.6)
    ax_cpu.set_yticks([0, 3, 6, 9, 12])
    ax_cpu.set_ylabel("")
    ax_cpu.set_xlabel("")
    ax_cpu.tick_params(labelbottom=False)

    # (b) memory over time (log scale: the three processes span two decades)
    header(ax_mem, "b", "Resident memory per container", "MiB, log scale (available: 9.7 GiB)")
    sns.lineplot(
        data=stats, x="t", y="mem_mib", hue="service", hue_order=order, palette=palette,
        estimator=None, sort=True, linewidth=1.5, legend=False, ax=ax_mem, zorder=3,
    )
    ax_mem.set_yscale("log")
    ax_mem.set_ylim(6, 1500)
    ax_mem.yaxis.set_major_locator(ticker.FixedLocator([10, 30, 100, 300, 1000]))
    ax_mem.yaxis.set_major_formatter(ticker.FuncFormatter(lambda v, _: f"{v:,.0f}"))
    ax_mem.yaxis.set_minor_locator(ticker.NullLocator())
    ax_mem.set_ylabel("")
    ax_mem.set_xlim(-6, run.end_s + 4)
    ax_mem.set_xticks(np.arange(0, run.end_s + 1, 20))
    ax_mem.set_xlabel("Seconds since the start of the k6 run (shaded: 30 s hold phases)")
    for svc in order:
        s = stats[stats.service == svc]
        for t, c, m in zip(s["t"], s["cores"], s["mem_mib"]):
            record(fid, "a", f"{svc}_cores", round(t, 1), c, "cores")
            record(fid, "b", f"{svc}_mem", round(t, 1), m, "MiB")

    # (c) CPU cost of one query, stacked by process
    tidy_axes(ax_q)
    header(ax_q, "c", "CPU cost of one query", "server CPU per request, core-ms")
    tput = run.levels.set_index("vus")["throughput_rps"]
    levels = [w[0] for w in run.windows]
    pos = np.arange(len(levels))
    bottom = np.zeros(len(levels))
    shares = {}
    for svc in order:
        vals = np.array([res[(res.vus == v) & (res.service == svc)]["cores"].iloc[0] / tput[v] * 1e3 for v in levels])
        ax_q.bar(pos, vals, 0.5, bottom=bottom, color=ENTITY[svc], edgecolor=SURFACE, linewidth=1.2, zorder=3)
        shares[svc] = vals
        for v, val in zip(levels, vals):
            record(fid, "c", f"{svc}_core_ms_per_query", v, val, "core-ms")
        bottom += vals
    share = shares["mongo"] / bottom
    for p, total, v in zip(pos, bottom, levels):
        ax_q.text(p, total + 0.8, f"{total:.1f}", ha="center", va="bottom", color=INK, fontsize=7)
    for v, sh in zip(levels, share):
        record(fid, "c", "mongod_share_of_query_cpu", v, sh * 100, "%")
    note(ax_q, -0.5, 53.5, f"mongod: {share.min() * 100:.0f}–{share.max() * 100:.0f} %\nof the server CPU\nof every query",
         va="top", ha="left", color=INK, linespacing=1.3)
    ax_q.set_xticks(pos, [str(v) for v in levels])
    ax_q.set_xlim(-0.6, len(levels) - 0.4)
    ax_q.set_ylim(0, 54)
    ax_q.set_yticks([0, 10, 20, 30, 40, 50])
    ax_q.set_xlabel("Concurrent clients (VUs)")

    handles = [Line2D([], [], color=ENTITY[s], lw=2.2, label=ENTITY_LABEL[s]) for s in order]
    handles.append(Patch(facecolor=PHASE, edgecolor="none", label="hold phase (steady state)"))
    fig.legend(handles=handles, loc="outside upper center", ncol=4, handlelength=1.6, columnspacing=1.6)
    save(fig, out, fid)


# --------------------------------------------------------------------------------------
# Figure 4 - replication impact
# --------------------------------------------------------------------------------------
def fig_replication(one: K6Run, two: K6Run, out: Path) -> None:
    fid = "fig4_replication"
    a, b = one.levels, two.levels
    n = a["vus"].to_numpy(float)

    def pair(ax, col):
        ax.plot(n, a[col], color=TEAL_LIGHT, linewidth=3.6, marker="o", markersize=7.2,
                markeredgecolor=SURFACE, markeredgewidth=RING, zorder=3, solid_capstyle="round")
        ax.plot(n, b[col], color=TEAL_DARK, linewidth=1.1, marker="D", markersize=3.6,
                markerfacecolor=SURFACE, markeredgecolor=TEAL_DARK, markeredgewidth=1.0, zorder=4)
        for vi, y1, y2 in zip(n, a[col], b[col]):
            record(fid, col, "1_replica", vi, y1, "")
            record(fid, col, "2_replicas", vi, y2, "")

    fig = new_figure(2.5)
    gs = fig.add_gridspec(1, 3, width_ratios=[1, 1, 1.05])

    ax = fig.add_subplot(gs[0])
    tidy_axes(ax)
    header(ax, "a", "Throughput", "requests per second")
    pair(ax, "throughput_rps")
    note(ax, 55, 268, "same plateau ≈ 250 req/s", ha="center", va="bottom", color=INK)
    ax.set_ylim(0, 320)
    ax.set_yticks([0, 100, 200, 300])

    ax = fig.add_subplot(gs[1])
    tidy_axes(ax)
    header(ax, "b", "95th-percentile latency", "milliseconds")
    threshold(ax, 500)
    note(ax, 2, 512, "objective  500 ms", va="bottom", color=INK)
    pair(ax, "p95_ms")
    ax.set_ylim(0, 800)
    ax.set_yticks([0, 200, 400, 600, 800])
    for axis in fig.axes:
        axis.set_xlim(-4, 106)
        axis.set_xticks([1, 10, 50, 100])
        axis.set_xlabel("Concurrent clients (VUs)")
    handles = [
        Line2D([], [], color=TEAL_LIGHT, lw=3.6, marker="o", ms=7.2, mec=SURFACE, mew=RING, label="1 Search replica"),
        Line2D([], [], color=TEAL_DARK, lw=1.1, marker="D", ms=3.6, mfc=SURFACE, mec=TEAL_DARK, label="2 Search replicas"),
    ]
    fig.axes[0].legend(handles=handles, loc="lower right", bbox_to_anchor=(1.0, 0.04))

    # (c) relative change, dot plot with a +/- 5 % reference band
    ax = fig.add_subplot(gs[2])
    tidy_axes(ax, grid="")
    header(ax, "c", "Change with 2 replicas", "relative to 1 replica, %")
    top = len(n) - 0.45
    ax.fill_betweenx([-0.7, top], -5, 5, color=PHASE, zorder=0, linewidth=0)
    ax.vlines(0, -0.7, top, color=INK_2, linewidth=0.8, zorder=1)
    y = np.arange(len(n))[::-1]
    d_tput = (b["throughput_rps"] / a["throughput_rps"] - 1) * 100
    d_p95 = (b["p95_ms"] / a["p95_ms"] - 1) * 100
    d_mean = (b["avg_ms"] / a["avg_ms"] - 1) * 100
    ax.plot(d_tput, y + 0.14, linestyle="none", marker="o", ms=5.2, color=INK, mec=SURFACE, mew=RING, zorder=4)
    ax.plot(d_p95, y - 0.14, linestyle="none", marker="D", ms=4.4, mfc=SURFACE, mec=INK, mew=1.1, zorder=4)
    for vi, t, p, m in zip(n, d_tput, d_p95, d_mean):
        record(fid, "c", "delta_throughput", vi, t, "%")
        record(fid, "c", "delta_p95", vi, p, "%")
        record(fid, "c", "delta_mean", vi, m, "%")
    ax.set_yticks(y, [f"{int(v)} VU" + ("s" if v > 1 else "") for v in n])
    ax.set_ylim(-0.7, len(n) - 0.3)
    ax.set_xlim(-8, 8)
    ax.set_xticks([-5, 0, 5], ["−5", "0", "+5"])
    ax.set_xlabel("Difference, % (shaded: ±5 %)")
    ax.legend(
        handles=[
            Line2D([], [], ls="none", marker="o", ms=5.2, color=INK, mec=SURFACE, label="throughput"),
            Line2D([], [], ls="none", marker="D", ms=4.4, mfc=SURFACE, mec=INK, label="p95 latency"),
        ],
        loc="upper left", bbox_to_anchor=(0.0, 1.0), ncol=2, columnspacing=0.8, handletextpad=0.2,
    )
    ax.set_ylim(-0.7, len(n) + 0.15)
    save(fig, out, fid)


# --------------------------------------------------------------------------------------
# Figure 5 - threading
# --------------------------------------------------------------------------------------
def speedup(df: pd.DataFrame, bench: str) -> pd.DataFrame:
    g = df[df["benchmark"] == bench].sort_values("threads")
    base = g.iloc[0]
    s = base["sec"] / g["sec"]
    err = s * np.sqrt(base["ci"] ** 2 + g["ci"] ** 2)  # relative CIs combined in quadrature
    return pd.DataFrame({"threads": g["threads"].to_numpy(), "speedup": s.to_numpy(), "err": err.to_numpy()})


def fig_threading(text: pd.DataFrame, datamart: pd.DataFrame, ingest: pd.DataFrame, out: Path) -> None:
    fid = "fig5_threading"
    fig = new_figure(2.5)
    gs = fig.add_gridspec(1, 2)

    # (a) CPU-bound text pipeline
    ax = fig.add_subplot(gs[0])
    tidy_axes(ax)
    header(ax, "a", "CPU-bound: text pipeline", "speed-up over 1 thread")
    reference(ax, [0.6, 8.4], [0.6, 8.4])
    ax.axvline(6, color=INK, linewidth=0.7, linestyle=(0, (1, 1.6)), zorder=1)
    note(ax, 6.12, 0.35, "6 physical\ncores", va="bottom", ha="left", color=INK)
    note(ax, 7.0, 7.45, "ideal", ha="right", va="bottom", rotation=0)
    styles = {
        "ProcessText": (ENTITY["indexing"], "o", LW, MS, "ProcessText (tokenise body)"),
        "ExtractMetadata": ("#6B7686", "^", 1.1, 4.4, "ExtractMetadata (header)"),
        "SplitBookText": ("#A0AAB8", "s", 1.1, 3.8, "SplitBookText (raw text)"),
    }
    handles = []
    for bench in ["ExtractMetadata", "SplitBookText", "ProcessText"]:
        color, marker, lw, ms, label = styles[bench]
        s = speedup(text, bench)
        ax.errorbar(s["threads"], s["speedup"], yerr=s["err"], color=color, linewidth=lw, marker=marker,
                    markersize=ms, markeredgecolor=SURFACE, markeredgewidth=RING, elinewidth=0.8, capsize=0,
                    zorder=4 if bench == "ProcessText" else 3)
        handles.append(Line2D([], [], color=color, lw=lw, marker=marker, ms=ms, mec=SURFACE, label=label))
        for t, v, e in zip(s["threads"], s["speedup"], s["err"]):
            record(fid, "a", bench, t, v, "x", f"95% CI +/- {e:.2f}")
    s = speedup(text, "ProcessText")
    note(ax, 8.25, s["speedup"].iloc[-1], f"{s['speedup'].iloc[-1]:.1f}×", va="center", ha="left", color=INK,
         fontsize=7)
    ax.legend(handles=handles[::-1], loc="upper left", bbox_to_anchor=(0, 1.0), handlelength=2.4)
    ax.set_xlim(0.5, 9.0)
    ax.set_ylim(0, 8.6)
    ax.set_xticks([1, 2, 4, 8])
    ax.set_yticks([0, 2, 4, 6, 8])
    ax.set_xlabel("Threads (GOMAXPROCS)")

    # (b) I/O-bound ingestion vs datamart-bound operations
    ax = fig.add_subplot(gs[1])
    tidy_axes(ax)
    header(ax, "b", "I/O-bound scales, datamart-bound does not", "speed-up over 1 worker or thread")
    reference(ax, [0.6, 8.4], [0.6, 8.4])
    curves = [speedup(datamart, b).assign(benchmark=b) for b in datamart["benchmark"].unique()]
    band = pd.concat(curves).groupby("threads")["speedup"].agg(["min", "median", "max"])
    ax.fill_between(band.index, band["min"], band["max"], color=ENTITY["mongo"], alpha=0.22, linewidth=0, zorder=2)
    line(ax, band.index, band["median"], ENTITY["mongo"], zorder=3)
    for t, row in band.iterrows():
        record(fid, "b", "datamart_speedup_min", t, row["min"], "x", "5 benchstat benchmarks")
        record(fid, "b", "datamart_speedup_max", t, row["max"], "x")
    w = ingest["Workers"].to_numpy(float)
    rate = ingest["BooksPerS"].to_numpy(float)
    line(ax, w, rate / rate[0], ENTITY["ingestion"])
    for wi, ri in zip(w, rate):
        record(fid, "b", "ingestion_speedup", wi, ri / rate[0], "x", f"{ri:.2f} books/s (other host)")
    note(ax, 8.25, rate[-1] / rate[0], f"{rate[-1] / rate[0]:.1f}×", va="center", ha="left", color=INK, fontsize=7)
    note(ax, 8.25, band["median"].iloc[-1], f"{band['median'].iloc[-1]:.1f}×", va="center", ha="left",
         color=INK, fontsize=7)
    note(ax, 7.0, 7.45, "ideal", ha="right", va="bottom")
    note(ax, 4.6, band["min"].min() - 0.3,
         f"all five datamart benchmarks: {band['min'].min():.2f}–{band['max'].max():.2f}×",
         ha="center", va="top", color=INK_2)
    ax.legend(
        handles=[
            Line2D([], [], color=ENTITY["ingestion"], lw=LW, marker="o", ms=MS, mec=SURFACE,
                   label="Ingestion: download workers*"),
            Line2D([], [], color=ENTITY["mongo"], lw=LW, marker="o", ms=MS, mec=SURFACE,
                   label="Datamart ops: client threads"),
        ],
        loc="upper left", bbox_to_anchor=(0, 1.0), handlelength=2.4,
    )
    ax.set_xlim(0.5, 9.0)
    ax.set_ylim(0, 8.6)
    ax.set_xticks([1, 2, 4, 8])
    ax.set_yticks([0, 2, 4, 6, 8])
    ax.set_xlabel("Workers or threads   (*measured on another host)")
    save(fig, out, fid)


# --------------------------------------------------------------------------------------
def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--evidence", type=Path, default=REPO / "docs" / "evidence")
    parser.add_argument("--out", type=Path, default=REPO / "docs" / "figures")
    parser.add_argument(
        "--only", nargs="*", choices=["latency", "indexing", "resources", "replication", "threading"],
        help="generate only these figures",
    )
    args = parser.parse_args(argv)
    ev, out = args.evidence, args.out
    wanted = set(args.only or ["latency", "indexing", "resources", "replication", "threading"])

    apply_style()
    search_1 = load_k6(find_one(ev, "search_benchmark_*_1replica.csv"))
    search_2 = load_k6(find_one(ev, "search_benchmark_*_2replicas.csv"))
    indexing = load_k6(find_one(ev, "indexing_benchmark_*.csv"))
    stats_1 = load_docker_stats(ev / "search_resources.csv", search_1)
    stats_2 = load_docker_stats(ev / "search_resources_2replicas.csv", search_2)
    stats_idx = load_docker_stats(ev / "indexing_resources.csv", indexing)
    res_1, res_2, res_idx = per_level(stats_1, search_1), per_level(stats_2, search_2), per_level(stats_idx, indexing)
    for label, res in (("search_1replica", res_1), ("search_2replicas", res_2), ("indexing", res_idx)):
        for _, r in res.iterrows():
            for metric in ("cores", "cores_sd", "mem_mib", "mem_max", "samples"):
                record("resources_per_level", label, f"{r['service']}_{metric}", r["vus"], r[metric], "")

    print(f"Evidence: {ev}\nFigures:  {out}")
    if "latency" in wanted:
        fig_query_latency(search_1, res_1, out)
    if "indexing" in wanted:
        fig_indexing(indexing, res_idx, out)
    if "resources" in wanted:
        fig_resources(search_1, stats_1, res_1, out)
    if "replication" in wanted:
        fig_replication(search_1, search_2, out)
    if "threading" in wanted:
        fig_threading(
            load_benchstat(ev / "benchstat_micro.csv"),
            load_benchstat(ev / "benchstat_cpu.csv"),
            load_ingestion(ev / "ingestion_throughput_summary.csv"),
            out,
        )
    table = pd.DataFrame(RECORDS)
    table.to_csv(out / "chart_data.csv", index=False, float_format="%.6g")
    print(f"  wrote {out / 'chart_data.csv'} ({len(table)} rows)")
    derived = table[table["panel"] == "derived"]
    for _, r in derived.iterrows():
        print(f"  {r['series']:<32} {r['y']:>10.3f} {r['unit']}  {r['note']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
