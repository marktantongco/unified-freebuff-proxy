#!/usr/bin/env python3
"""bench_parallel_search.py — benchmark Parallel Search modes on YOUR queries.

Measures p50/p90 latency and per-mode cost for turbo / fast / basic / advanced
across a query set (built-in defaults or --queries-file, one query per line).
Optionally validates result quality via excerpt keyword-hit rate.

Usage:
    export PARALLEL_API_KEY=...
    python3 scripts/bench_parallel_search.py                      # 2 warmup + 5 runs/mode
    python3 scripts/bench_parallel_search.py --runs 10 --warmup 1
    python3 scripts/bench_parallel_search.py --queries-file my_queries.txt
    python3 scripts/bench_parallel_search.py --modes turbo fast   # subset
    python3 scripts/bench_parallel_search.py --concurrency 4      # parallel requests

Notes:
    * Each call costs real credits: turbo/fast $1/1k, basic/advanced $5/1k.
      Default run = (2 warmup + 5 runs) x 4 modes x 5 queries = 140 calls ≈ $0.36.
    * Latency here is end-to-end from this box (includes your network RTT);
      official figures: turbo ~200ms, fast ~700ms, basic ~1s, advanced ~3s.
"""

import argparse
import concurrent.futures as cf
import json
import os
import statistics
import sys
import time

try:
    from parallel import Parallel
except ImportError:
    print("pip install 'parallel-web>=1.0.1'", file=sys.stderr)
    sys.exit(2)

DEFAULT_QUERIES = [
    "What are the latest benchmarks for vector databases?",
    "Current price and specs of the Framework Laptop 16",
    "Who won the most recent Formula 1 race and by what margin?",
    "Best practices for Go gRPC streaming backpressure",
    "Recent CVEs affecting nginx 1.27",
]

MODE_COST_PER_1K = {"turbo": 1.0, "fast": 1.0, "basic": 5.0, "advanced": 5.0}
KEYWORDS = {  # rough relevance signal per built-in query
    0: {"benchmark", "vector", "qdrant", "pgvector", "pinecone", "weaviate", "milvus"},
    1: {"framework", "laptop", "16", "price", "spec", "ryzen", "battery"},
    2: {"f1", "formula", "grand", "prix", "win", "pole", "lap"},
    3: {"grpc", "streaming", "backpressure", "go", "golang", "flow"},
    4: {"nginx", "cve", "1.27", "vulnerab", "security", "patch"},
}


def load_queries(path):
    if not path:
        return DEFAULT_QUERIES
    with open(path) as f:
        qs = [ln.strip() for ln in f if ln.strip() and not ln.startswith("#")]
    return qs or DEFAULT_QUERIES


def make_queries_for(q):
    stop = set("""what who when where why how is are the a an of for in on to and or
               with without from about into over under best top current recent latest""".split())
    words = [w.strip("?,.:!\"'()") for w in q.split() if w.lower() not in stop and len(w) > 1]
    words = words[:6] or [q[:40]]
    mid = len(words) // 2
    return [
        " ".join(words[: max(3, mid + 1)]),
        " ".join(words[mid:]),
        " ".join(words[:3]) + " 2026",
    ]


def run_once(client, query, mode, session_id):
    t0 = time.perf_counter()
    try:
        res = client.search(
            objective=query,
            search_queries=make_queries_for(query),
            mode=mode,
            client_model="gpt-5.4",
            session_id=session_id,
        )
        dt = (time.perf_counter() - t0) * 1000
        n_results = len(res.results)
        text = " ".join(ex for r in res.results for ex in (r.excerpts or [])).lower()
        return dt, n_results, text, None
    except Exception as e:  # noqa: BLE001 — record and continue
        return (time.perf_counter() - t0) * 1000, 0, "", str(e)


def pct(sorted_vals, p):
    if not sorted_vals:
        return 0.0
    k = min(len(sorted_vals) - 1, max(0, int(round((p / 100) * (len(sorted_vals) - 1)))))
    return sorted_vals[k]


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--queries-file", default=None)
    ap.add_argument("--modes", nargs="+", default=["turbo", "fast", "basic", "advanced"],
                    choices=list(MODE_COST_PER_1K))
    ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--warmup", type=int, default=2)
    ap.add_argument("--concurrency", type=int, default=1)
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    if not os.environ.get("PARALLEL_API_KEY"):
        print("Set PARALLEL_API_KEY (https://platform.parallel.ai)", file=sys.stderr)
        sys.exit(2)

    queries = load_queries(args.queries_file)
    client = Parallel()
    results = {m: {"latencies": [], "counts": [], "errors": 0, "hits": []} for m in args.modes}

    est = len(args.modes) * len(queries) * (args.warmup + args.runs)
    cheap = sum(MODE_COST_PER_1K[m] for m in args.modes) / 1000
    print(f"plan: {est} calls ≈ ${est * cheap / len(args.modes):.2f} "
          f"({args.warmup}+{args.runs} per mode per query)", file=sys.stderr)

    for mode in args.modes:
        # Warmup (JIT-ish DNS/TLS/handshake, excluded from stats).
        for q in queries[:max(1, min(args.warmup, len(queries)))]:
            run_once(client, q, mode, f"bench_warm_{mode}")

        for qi, q in enumerate(queries):
            session = f"bench_{mode}_{qi}_{int(time.time())}"
            if args.concurrency > 1:
                with cf.ThreadPoolExecutor(args.concurrency) as ex:
                    futs = [ex.submit(run_once, client, q, mode, session)
                            for _ in range(args.runs)]
                    for f in futs:
                        dt, n, text, err = f.result()
                        rec(results[mode], dt, n, text, err, qi)
            else:
                for _ in range(args.runs):
                    dt, n, text, err = run_once(client, q, mode, session)
                    rec(results[mode], dt, n, text, err, qi)

    report(results, queries, args)


def rec(bucket, dt, n, text, err, qi):
    if err:
        bucket["errors"] += 1
        return
    bucket["latencies"].append(dt)
    bucket["counts"].append(n)
    kw = KEYWORDS.get(qi)
    if kw:
        bucket["hits"].append(sum(1 for k in kw if k in text) / len(kw))


def report(results, queries, args):
    if args.json:
        out = {}
        for m, b in results.items():
            lat = sorted(b["latencies"])
            out[m] = {
                "p50_ms": round(pct(lat, 50)),
                "p90_ms": round(pct(lat, 90)),
                "mean_ms": round(statistics.mean(lat)) if lat else 0,
                "results_p50": statistics.median(b["counts"]) if b["counts"] else 0,
                "keyword_hit_rate": round(statistics.mean(b["hits"]), 3) if b["hits"] else None,
                "errors": b["errors"],
                "cost_per_1k": MODE_COST_PER_1K[m],
            }
        print(json.dumps(out, indent=2))
        return

    print(f"\n{'mode':<10}{'p50':>8}{'p90':>9}{'mean':>8}{'res':>6}{'kw-hit':>8}{'err':>5}{'$/1k':>7}")
    print("-" * 61)
    for m, b in results.items():
        lat = sorted(b["latencies"])
        p50, p90 = pct(lat, 50), pct(lat, 90)
        mean = statistics.mean(lat) if lat else 0
        res_p50 = statistics.median(b["counts"]) if b["counts"] else 0
        kw = statistics.mean(b["hits"]) if b["hits"] else None
        print(f"{m:<10}{p50:>7.0f}m{p90:>8.0f}m{mean:>7.0f}m{res_p50:>6.1f}"
              f"{(f'{kw:>8.0%}' if kw is not None else ' ' * 8)}"
              f"{b['errors']:>5}{MODE_COST_PER_1K[m]:>7.0f}")
    print("\nLatency measured end-to-end from this box (network RTT included).")


if __name__ == "__main__":
    main()
