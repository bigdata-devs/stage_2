#!/usr/bin/env python3
"""Generate the report figures from the Stage 2 benchmark evidence.

Reads the official measurements under docs/evidence/ (k6 CSVs, benchstat CSVs,
resource samples and the Control Module coordination logs) and writes one PNG at
300 dpi plus one vector PDF per figure into docs/evidence/figures/. Run it from
the repository root:

    python3 scripts/plot_benchmarks.py

Only pandas and matplotlib are required. Every figure is generated from a file
committed to the repository, so the whole set can be regenerated at any time.
Figures whose source data is a validation re-run are skipped with a warning when
that data is absent.
"""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path

import matplotlib

matplotlib.use("Agg")

import matplotlib.pyplot as plt
import pandas as pd

EVIDENCE = Path("docs/evidence")
FIGURES = EVIDENCE / "figures"

INDEXING_K6 = EVIDENCE / "indexing_benchmark_2026-11-01T16-14-15-954Z.csv"
SEARCH_1R_K6 = EVIDENCE / "search_benchmark_2026-11-01T16-26-57-365Z_1replica.csv"
SEARCH_2R_K6 = EVIDENCE / "search_benchmark_2026-11-01T16-30-41-872Z_2replicas.csv"
BENCHSTAT_DATAMART = EVIDENCE / "benchstat.csv"
BENCHSTAT_DATAMART_CPU = EVIDENCE / "benchstat_cpu.csv"
BENCHSTAT_TEXT = EVIDENCE / "benchstat_micro.csv"
INDEXING_RESOURCES = EVIDENCE / "indexing_resources.csv"
SEARCH_RESOURCES_RERUN = EVIDENCE / "search_resources_host.csv"
HOST_RERUN = EVIDENCE / "host_search.csv"
INGESTION_SUMMARY = EVIDENCE / "ingestion_throughput_summary.csv"
COORDINATION_DIR = Path("cmd/control/results")

DPI = 300


def save(fig: plt.Figure, name: str) -> None:
    FIGURES.mkdir(parents=True, exist_ok=True)
    fig.savefig(FIGURES / f"{name}.png", dpi=DPI, bbox_inches="tight")
    fig.savefig(FIGURES / f"{name}.pdf", bbox_inches="tight")
    plt.close(fig)
    print(f"wrote {name}.png and {name}.pdf")


def read_benchstat_sec_op(path: Path) -> dict[str, float]:
    """Return {benchmark name: sec/op} from a `benchstat -format csv` file.

    The file stacks several blocks (sec/op, B/op, ...) each introduced by a line
    that starts with a comma; only the sec/op block of every block pair is kept.
    """
    sec_op: dict[str, float] = {}
    metric = None
    with path.open(encoding="utf-8") as handle:
        for raw in handle:
            line = raw.rstrip("\n")
            if not line:
                metric = None
                continue
            if line.startswith(","):
                header = line.split(",")[1]
                metric = header if header == "sec/op" else None
                continue
            if metric == "sec/op":
                name, value, *_ = line.split(",")
                if name != "geomean":
                    sec_op[name] = float(value)
    return sec_op


def threads_of(benchmark: str) -> int:
    """Go suffixes benchmark names with the GOMAXPROCS value; cpu=1 has no suffix."""
    _, _, suffix = benchmark.rpartition("-")
    return int(suffix) if suffix.isdigit() else 1


def base_name(benchmark: str) -> str:
    head, _, suffix = benchmark.rpartition("-")
    return head if suffix.isdigit() else benchmark


def seconds(duration: str) -> float:
    """Convert Go duration strings like '586ms', '1.412s' or '3m39.169s' to seconds."""
    match = re.fullmatch(r"(?:(\d+)m)?([\d.]+)(ms|s|µs|us)", duration)
    if not match:
        raise ValueError(f"unparsable duration: {duration}")
    minutes = int(match.group(1) or 0)
    value = float(match.group(2))
    unit = match.group(3)
    scale = {"s": 1.0, "ms": 1e-3, "µs": 1e-6, "us": 1e-6}[unit]
    return minutes * 60 + value * scale


def memory_mb(raw: str) -> float:
    """Convert docker stats memory strings like '22.91MiB' or '1.2GiB' to MB."""
    match = re.fullmatch(r"([\d.]+)([KMG]?i?)B", raw.strip())
    if not match:
        raise ValueError(f"unparsable memory value: {raw}")
    value = float(match.group(1))
    scale = {
        "": 1.0 / (1024 * 1024),
        "K": 1.0 / 1024,
        "Ki": 1.0 / 1024,
        "M": 1.0,
        "Mi": 1.0,
        "G": 1024.0,
        "Gi": 1024.0,
    }
    return value * scale[match.group(2)]


def elapsed_seconds(stamps: pd.Series, origin: pd.Timestamp) -> pd.Series:
    return (pd.to_datetime(stamps, utc=True) - origin).dt.total_seconds()


def plot_indexing_throughput() -> None:
    data = pd.read_csv(INDEXING_K6)
    positions = list(range(len(data)))
    fig, axis = plt.subplots(figsize=(7, 4.2))
    bars = axis.bar(
        positions,
        data["books_per_second"],
        color="#4C78A8",
        width=0.55,
        label="Books indexed per second",
    )
    axis.bar_label(bars, fmt="%.2f", padding=3, fontsize=10)
    axis.set_xticks(positions)
    axis.set_xticklabels(data["vus"].astype(str))
    axis.set_xlabel("Concurrent clients (k6 VUs)")
    axis.set_ylabel("Books per second")
    axis.set_ylim(0, data["books_per_second"].max() * 1.35)
    axis.grid(axis="y", alpha=0.3)

    latency = axis.twinx()
    latency.plot(
        positions,
        data["p95_ms"],
        color="#E45756",
        marker="o",
        linewidth=2,
        label="p95 latency",
    )
    latency.set_ylabel("p95 latency (ms)", color="#E45756")
    latency.tick_params(axis="y", labelcolor="#E45756")
    handles1, labels1 = axis.get_legend_handles_labels()
    handles2, labels2 = latency.get_legend_handles_labels()
    axis.legend(handles1 + handles2, labels1 + labels2, loc="upper left")
    fig.tight_layout()
    save(fig, "fig1_indexing_throughput")


def plot_search_throughput_replicas() -> None:
    one = pd.read_csv(SEARCH_1R_K6)
    two = pd.read_csv(SEARCH_2R_K6)
    positions = range(len(one))
    width = 0.38
    fig, axis = plt.subplots(figsize=(7.5, 4.2))
    first = axis.bar(
        [p - width / 2 for p in positions],
        one["throughput_rps"],
        width,
        color="#4C78A8",
        label="1 replica",
    )
    second = axis.bar(
        [p + width / 2 for p in positions],
        two["throughput_rps"],
        width,
        color="#F58518",
        label="2 replicas",
    )
    axis.bar_label(first, fmt="%.0f", padding=3, fontsize=9)
    axis.bar_label(second, fmt="%.0f", padding=3, fontsize=9)
    axis.set_xticks(list(positions))
    axis.set_xticklabels(one["vus"].astype(str))
    axis.set_xlabel("Concurrent clients (k6 VUs)")
    axis.set_ylabel("Search requests per second")
    axis.set_ylim(0, max(one["throughput_rps"].max(), two["throughput_rps"].max()) * 1.2)
    axis.grid(axis="y", alpha=0.3)
    axis.legend()
    fig.tight_layout()
    save(fig, "fig2_search_throughput_replicas")


def plot_search_latency_replicas() -> None:
    one = pd.read_csv(SEARCH_1R_K6)
    two = pd.read_csv(SEARCH_2R_K6)
    fig, axis = plt.subplots(figsize=(7.5, 4.2))
    axis.plot(one["vus"], one["avg_ms"], marker="o", color="#4C78A8", label="avg, 1 replica")
    axis.plot(one["vus"], one["p95_ms"], marker="s", color="#4C78A8", linestyle="--", label="p95, 1 replica")
    axis.plot(two["vus"], two["avg_ms"], marker="o", color="#F58518", label="avg, 2 replicas")
    axis.plot(two["vus"], two["p95_ms"], marker="s", color="#F58518", linestyle="--", label="p95, 2 replicas")
    axis.set_xlabel("Concurrent clients (k6 VUs)")
    axis.set_ylabel("Query latency (ms)")
    axis.set_xticks(list(one["vus"]))
    axis.grid(alpha=0.3)
    axis.legend()
    fig.tight_layout()
    save(fig, "fig3_search_latency_replicas")


def read_coordination_runs() -> pd.DataFrame:
    rows = []
    for path in sorted(COORDINATION_DIR.glob("coordination_workers_*.txt")):
        text = path.read_text(encoding="utf-8")
        workers = int(re.search(r"starting run.*workers=(\d+)", text).group(1))
        finished = re.search(r"run finished\" (.*)", text).group(1)
        fields = dict(re.findall(r"(\w+)=([^\s]+)", finished))
        rows.append(
            {
                "workers": workers,
                "books_per_second": float(fields["books_per_second"]),
                "indexed": int(fields["indexed"]),
                "failed": int(fields["failed"]),
                "ingest_avg_ms": seconds(fields["ingest_step_avg"]) * 1000,
                "index_avg_s": seconds(fields["index_step_avg"]),
            }
        )
    return pd.DataFrame(rows).sort_values("workers").reset_index(drop=True)


def plot_coordination_workers() -> None:
    data = read_coordination_runs()
    fig, axis = plt.subplots(figsize=(7, 4.2))
    bars = axis.bar(
        data["workers"],
        data["books_per_second"],
        color="#54A24B",
        width=0.55,
        label="Pipeline books per second",
    )
    axis.bar_label(bars, fmt="%.2f", padding=3, fontsize=10)
    axis.set_xticks(list(data["workers"]))
    axis.set_xlabel("Control Module workers (-workers)")
    axis.set_ylabel("Books per second")
    axis.set_ylim(0, data["books_per_second"].max() * 1.45)
    axis.grid(axis="y", alpha=0.3)

    cost = axis.twinx()
    cost.plot(
        data["workers"],
        data["index_avg_s"],
        color="#E45756",
        marker="o",
        linewidth=2,
        label="Avg index step",
    )
    cost.set_ylabel("Average index step (s)", color="#E45756")
    cost.tick_params(axis="y", labelcolor="#E45756")
    axis.annotate(
        "workers=2: 22 books\nunavailable on Gutenberg",
        xy=(2, data.loc[data["workers"] == 2, "books_per_second"].iloc[0] + 0.03),
        xytext=(1.35, 0.66),
        fontsize=8.5,
        color="#555555",
        ha="left",
        arrowprops={"arrowstyle": "->", "color": "#555555", "lw": 0.8},
    )
    handles1, labels1 = axis.get_legend_handles_labels()
    handles2, labels2 = cost.get_legend_handles_labels()
    axis.legend(handles1 + handles2, labels1 + labels2, loc="upper left")
    fig.tight_layout()
    save(fig, "fig4_coordination_workers")


def plot_search_resources_host() -> None:
    if not (HOST_RERUN.exists() and SEARCH_RESOURCES_RERUN.exists()):
        print(f"warning: skipping fig5, {HOST_RERUN.name} or {SEARCH_RESOURCES_RERUN.name} absent")
        return
    vcpus = os.cpu_count() or 1
    host = pd.read_csv(HOST_RERUN)
    containers = pd.read_csv(SEARCH_RESOURCES_RERUN)
    origin = min(pd.to_datetime(host["timestamp_utc"], utc=True).min(),
                 pd.to_datetime(containers["timestamp_utc"], utc=True).min())
    host_t = elapsed_seconds(host["timestamp_utc"], origin)
    host["cpu_cores"] = host["cpu_busy_pct"].astype(float) / 100.0 * vcpus
    containers["cpu_cores"] = containers["cpu_percent"].astype(float) / 100.0
    containers["mem_mb"] = containers["mem_used"].map(memory_mb)
    containers["elapsed"] = elapsed_seconds(containers["timestamp_utc"], origin)

    fig, (cpu_axis, mem_axis) = plt.subplots(2, 1, figsize=(8, 6.4), sharex=True)
    container_colors = {"mongo": "#F58518", "nginx": "#54A24B", "search": "#E45756"}
    cpu_axis.plot(host_t, host["cpu_cores"], color="#4C78A8", linewidth=2,
                  label=f"Whole host ({vcpus} vCPUs)")
    for name, frame in containers.groupby("container"):
        label = name.removeprefix("boogle-engine-").removesuffix("-1")
        color = container_colors.get(label)
        cpu_axis.plot(frame["elapsed"], frame["cpu_cores"], linewidth=1.5,
                      color=color, label=f"{label} container")
    cpu_axis.set_ylabel("CPU cores busy")
    cpu_axis.grid(alpha=0.3)
    cpu_axis.legend(fontsize=8.5)

    mem_axis.plot(host_t, host["mem_used_mb"], color="#4C78A8", linewidth=2, label="Whole host")
    for name, frame in containers.groupby("container"):
        label = name.removeprefix("boogle-engine-").removesuffix("-1")
        color = container_colors.get(label)
        mem_axis.plot(frame["elapsed"], frame["mem_mb"], linewidth=1.5,
                      color=color, label=f"{label} container")
    mem_axis.set_ylabel("Memory used (MB)")
    mem_axis.set_xlabel("Seconds since the start of the run")
    mem_axis.grid(alpha=0.3)
    mem_axis.legend(fontsize=8.5)
    fig.tight_layout()
    save(fig, "fig5_search_resources_host")


def plot_indexing_resources() -> None:
    data = pd.read_csv(INDEXING_RESOURCES)
    origin = pd.to_datetime(data["timestamp_utc"], utc=True).min()
    data["elapsed"] = elapsed_seconds(data["timestamp_utc"], origin)
    data["cpu_cores"] = data["cpu_percent"].astype(float) / 100.0
    data["mem_mb"] = data["mem_used"].map(memory_mb)

    fig, (cpu_axis, mem_axis) = plt.subplots(2, 1, figsize=(8, 6.4), sharex=True)
    for name, frame in data.groupby("container"):
        label = name.removeprefix("boogle-engine-").removesuffix("-1")
        cpu_axis.plot(frame["elapsed"], frame["cpu_cores"], linewidth=1.5, label=label)
        mem_axis.plot(frame["elapsed"], frame["mem_mb"], linewidth=1.5, label=label)
    cpu_axis.set_ylabel("CPU cores busy")
    cpu_axis.grid(alpha=0.3)
    cpu_axis.legend(fontsize=8.5)
    mem_axis.set_ylabel("Memory used (MB)")
    mem_axis.set_xlabel("Seconds since the start of the run")
    mem_axis.grid(alpha=0.3)
    mem_axis.legend(fontsize=8.5)
    fig.tight_layout()
    save(fig, "fig6_indexing_resources")


TEXT_LABELS = {
    "ProcessText": "Tokenize body",
    "ExtractMetadata": "Parse header",
    "SplitBookText": "Split book",
}


def plot_text_pipeline_threads() -> None:
    sec_op = read_benchstat_sec_op(BENCHSTAT_TEXT)
    fig, axis = plt.subplots(figsize=(7, 4.2))
    for benchmark, label in TEXT_LABELS.items():
        points = sorted(
            (threads_of(name), value)
            for name, value in sec_op.items()
            if base_name(name) == benchmark
        )
        axis.plot(
            [p[0] for p in points],
            [p[1] * 1000 for p in points],
            marker="o",
            linewidth=2,
            label=label,
        )
    axis.set_xlabel("Threads (GOMAXPROCS)")
    axis.set_ylabel("Time per book (ms, log scale)")
    axis.set_xticks([1, 2, 4, 8])
    axis.set_yscale("log")
    axis.grid(alpha=0.3, which="both")
    axis.legend()
    fig.tight_layout()
    save(fig, "fig7_text_pipeline_threads")


DATAMART_LABELS = {
    "PostingsForTerms": "Read posting list",
    "SearchBooksWithoutFilters": "Search, no filters",
    "SearchBooksWithAllFilters": "Search, all filters",
    "IndexBook": "Index synthetic book",
    "IndexBookRealBook": "Index real book (6 576 terms)",
}


def plot_datamart_costs() -> None:
    sec_op = read_benchstat_sec_op(BENCHSTAT_DATAMART)
    labels = [DATAMART_LABELS[base_name(name)] for name in sec_op]
    values = [sec_op[name] * 1000 for name in sec_op]
    fig, axis = plt.subplots(figsize=(7.5, 4.2))
    bars = axis.barh(labels[::-1], values[::-1], color="#4C78A8")
    axis.bar_label(bars, fmt="%.2f", padding=4, fontsize=9)
    axis.set_xlabel("Time per operation (ms, log scale)")
    axis.set_xscale("log")
    axis.grid(axis="x", alpha=0.3, which="both")
    axis.set_xlim(0.1, max(values) * 4)
    fig.tight_layout()
    save(fig, "fig8_datamart_costs")


def plot_datamart_threads() -> None:
    sec_op = read_benchstat_sec_op(BENCHSTAT_DATAMART_CPU)
    wanted = [
        "PostingsForTerms",
        "SearchBooksWithoutFilters",
        "SearchBooksWithAllFilters",
        "IndexBook",
    ]
    fig, axis = plt.subplots(figsize=(7, 4.2))
    for benchmark in wanted:
        points = sorted(
            (threads_of(name), value)
            for name, value in sec_op.items()
            if base_name(name) == benchmark
        )
        axis.plot(
            [p[0] for p in points],
            [p[1] * 1000 for p in points],
            marker="o",
            linewidth=2,
            label=DATAMART_LABELS[benchmark],
        )
    axis.set_xlabel("Threads (GOMAXPROCS)")
    axis.set_ylabel("Time per operation (ms, log scale)")
    axis.set_xticks([1, 2, 4, 8])
    axis.set_yscale("log")
    axis.grid(alpha=0.3, which="both")
    axis.legend(fontsize=8.5)
    fig.tight_layout()
    save(fig, "fig9_datamart_threads")


def plot_ingestion_workers() -> None:
    data = pd.read_csv(INGESTION_SUMMARY, encoding="utf-8-sig")
    numeric = data.apply(lambda column: column.astype(str).str.replace(",", ".", regex=False))
    workers = numeric["Workers"].astype(int)
    throughput = numeric["BooksPerS"].astype(float)
    fig, axis = plt.subplots(figsize=(7, 4.2))
    bars = axis.bar(workers.astype(str), throughput, color="#B279A2", width=0.55)
    axis.bar_label(bars, fmt="%.2f", padding=3, fontsize=10)
    axis.set_xlabel("Ingestion Service workers (-workers)")
    axis.set_ylabel("Books ingested per second")
    axis.set_ylim(0, throughput.max() * 1.25)
    axis.grid(axis="y", alpha=0.3)
    fig.tight_layout()
    save(fig, "fig10_ingestion_workers")


def main() -> int:
    plt.rcParams.update({"font.size": 11, "figure.dpi": DPI})
    required = [
        INDEXING_K6,
        SEARCH_1R_K6,
        SEARCH_2R_K6,
        BENCHSTAT_DATAMART,
        BENCHSTAT_DATAMART_CPU,
        BENCHSTAT_TEXT,
        INDEXING_RESOURCES,
        INGESTION_SUMMARY,
    ]
    missing = [str(path) for path in required if not path.exists()]
    if missing:
        print("missing evidence files:", *missing, sep="\n  ", file=sys.stderr)
        return 1
    plot_indexing_throughput()
    plot_search_throughput_replicas()
    plot_search_latency_replicas()
    plot_coordination_workers()
    plot_search_resources_host()
    plot_indexing_resources()
    plot_text_pipeline_threads()
    plot_datamart_costs()
    plot_datamart_threads()
    plot_ingestion_workers()
    print(f"all figures written to {FIGURES}/")
    return 0


if __name__ == "__main__":
    sys.exit(main())
