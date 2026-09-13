#!/usr/bin/env python3
"""parallel_research.py — cascading research pipeline for the Parallel APIs.

Cascade (cheapest/fastest first, escalate only when needed):

    1. Search  `fast`   (~700ms, $1/1k)   -> LLM-optimized excerpts
    2. Extract (batch,   $1/1k URLs)     -> focused excerpts from best URLs
    3. Task    `pro`    (~3.5min, $0.10) -> deep cited report, polled or webhook

Usage:
    export PARALLEL_API_KEY=...
    python3 scripts/parallel_research.py "your research question" [options]

Options:
    --cascade {auto,search,extract,task}   escalation policy (default auto)
    --mode MODE                            search mode: turbo|fast|basic|advanced (default fast)
    --task-processor PROC                  task processor if escalation reaches stage 3 (default pro)
    --webhook URL                          task webhook URL (disables blocking poll for stage 3)
    --max-urls N                           URLs to extract in stage 2 (default 5, max 20)
    --json                                 dump the full result object

Exit codes: 0 answered, 1 no answer, 2 config error.
"""

import argparse
import json
import os
import sys
import time
import uuid

try:
    from parallel import Parallel
except ImportError:
    print("pip install 'parallel-web>=1.0.1'  (import: from parallel import Parallel)",
          file=sys.stderr)
    sys.exit(2)

POLL_INTERVAL_S = 10
TASK_TIMEOUT_S = 3600  # 'pro' blocks fine under 10 min; keep headroom


def new_session():
    return f"cascade_{uuid.uuid4()}"


def stage_search(client, question, mode, session_id):
    """Stage 1: one Search call. Returns (answer_text, evidence, meta)."""
    t0 = time.time()
    search = client.search(
        objective=question,
        search_queries=_keyword_queries(question),
        mode=mode,
        client_model="gpt-5.4",
        session_id=session_id,
    )
    elapsed = time.time() - t0
    evidence = []
    for r in search.results:
        for ex in r.excerpts:
            evidence.append({"url": r.url, "title": r.title, "excerpt": ex})
    meta = {"stage": "search", "mode": mode, "latency_s": round(elapsed, 2),
            "results": len(search.results), "evidence_count": len(evidence)}
    return evidence, meta


def stage_extract(client, question, urls, session_id, max_urls):
    """Stage 2: batched Extract on the most promising URLs."""
    urls = urls[:max_urls]
    if not urls:
        return [], {"stage": "extract", "skipped": "no urls"}
    t0 = time.time()
    extract = client.extract(
        urls=urls,
        objective=question,
        session_id=session_id,
    )
    elapsed = time.time() - t0
    evidence = []
    for r in extract.results:
        text = getattr(r, "excerpts", None) or [getattr(r, "full_content", "") or ""]
        for ex in text:
            evidence.append({"url": r.url, "title": r.title, "excerpt": ex})
    failed = [e.url for e in (extract.errors or [])]
    meta = {"stage": "extract", "latency_s": round(elapsed, 2),
            "fetched": len(extract.results), "failed": failed}
    return evidence, meta


def stage_task(client, question, processor, webhook, session_id):
    """Stage 3: deep research Task run. Webhook (fire-and-forget) or blocking poll."""
    if webhook:
        run = client.task_run.create(
            input=question,
            processor=processor,
            webhook={"url": webhook, "event_types": ["task_run.status"]},
            metadata={"session_id": session_id, "pipeline": "cascade"},
        )
        return None, {"stage": "task", "run_id": run.run_id, "webhook": webhook,
                      "note": "result will arrive via webhook; verify HMAC signature"}
    t0 = time.time()
    run = client.task_run.create(input=question, processor=processor,
                                 metadata={"session_id": session_id, "pipeline": "cascade"})
    result = client.task_run.result(run.run_id, api_timeout=TASK_TIMEOUT_S)
    elapsed = time.time() - t0
    citations = sum(len(f.citations or []) for f in (result.output.basis or []))
    meta = {"stage": "task", "processor": processor, "run_id": run.run_id,
            "latency_s": round(elapsed, 1), "citations": citations}
    return result.output.content, meta


def _keyword_queries(question, n=3):
    """Cheap local heuristic: distinctive words -> 3 short keyword queries."""
    stop = set("""what who when where why how is are the a an of for in on to and or
               vs versus between with without from about into over under best top""".split())
    words = [w.strip("?,.:!\"'()") for w in question.split()]
    keys = [w for w in words if w.lower() not in stop and len(w) > 2][:6]
    if not keys:
        keys = [question[:50]]
    queries = [" ".join(keys[: max(3, len(keys) // 2)])]
    if len(keys) > 3:
        queries.append(" ".join(keys[3:]))
    queries.append(" ".join(keys[:3]) + " 2026")
    return queries[:n]


def main():
    ap = argparse.ArgumentParser(description="Cascade: Search fast -> Extract batch -> Task pro")
    ap.add_argument("question")
    ap.add_argument("--cascade", default="auto", choices=["auto", "search", "extract", "task"])
    ap.add_argument("--mode", default="fast", choices=["turbo", "fast", "basic", "advanced"])
    ap.add_argument("--task-processor", default="pro")
    ap.add_argument("--webhook", default=None)
    ap.add_argument("--max-urls", type=int, default=5)
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    if not os.environ.get("PARALLEL_API_KEY"):
        print("Set PARALLEL_API_KEY (https://platform.parallel.ai)", file=sys.stderr)
        sys.exit(2)

    client = Parallel()
    session_id = new_session()
    trace = []

    # Stage 1 — always search first.
    try:
        evidence, meta = stage_search(client, args.question, args.mode, session_id)
        trace.append(meta)
        print(f"[1/3] search({meta['mode']}): {meta['results']} results, "
              f"{meta['evidence_count']} excerpts in {meta['latency_s']}s", file=sys.stderr)
    except Exception as e:
        print(f"search failed: {e}", file=sys.stderr)
        evidence, meta = [], {"stage": "search", "error": str(e)}
        trace.append(meta)

    # Stage 2 — extract from top URLs.
    if args.cascade in ("auto", "extract", "task"):
        try:
            urls = [e["url"] for e in evidence]
            ev2, meta2 = stage_extract(client, args.question, urls, session_id, args.max_urls)
            evidence.extend(ev2)
            trace.append(meta2)
            print(f"[2/3] extract: {meta2.get('fetched', 0)} fetched, "
                  f"{len(meta2.get('failed', []))} failed in {meta2.get('latency_s', '?')}s",
                  file=sys.stderr)
        except Exception as e:
            trace.append({"stage": "extract", "error": str(e)})
            print(f"extract failed: {e}", file=sys.stderr)

    # Stage 3 — deep research task (auto: only if thin evidence).
    answer, task_meta = None, None
    thin = sum(len(e["excerpt"]) for e in evidence) < 2000
    if args.cascade == "task" or (args.cascade == "auto" and thin) or args.webhook:
        try:
            answer, task_meta = stage_task(client, args.question, args.task_processor,
                                           args.webhook, session_id)
            trace.append(task_meta)
            if answer is None:
                print(f"[3/3] task {args.task_processor} submitted: run_id={task_meta['run_id']} "
                      f"(webhook)", file=sys.stderr)
            else:
                print(f"[3/3] task {args.task_processor}: {task_meta['citations']} citations "
                      f"in {task_meta['latency_s']}s", file=sys.stderr)
        except Exception as e:
            trace.append({"stage": "task", "error": str(e)})
            print(f"task failed: {e}", file=sys.stderr)
    elif args.cascade == "auto":
        trace.append({"stage": "task", "skipped": "sufficient evidence from search+extract"})

    if answer is None:
        # Synthesize answer locally from gathered evidence (no extra API cost).
        answer = "\n\n".join(
            f"[{e['title'] or e['url']}]({e['url']})\n{e['excerpt']}" for e in evidence[:12]
        ) or "No evidence gathered."

    if args.json:
        print(json.dumps({"question": args.question, "session_id": session_id,
                          "answer": answer, "trace": trace,
                          "evidence": evidence[:30]}, indent=2))
    else:
        print(f"# {args.question}\n")
        print(answer)
        print("\n--- pipeline trace ---")
        for t in trace:
            print(json.dumps(t))

    sys.exit(0 if (answer and answer != "No evidence gathered.") else 1)


if __name__ == "__main__":
    main()
