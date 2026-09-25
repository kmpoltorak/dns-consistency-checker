# Architecture

This document describes how `dns-consistency-checker` is built. User-facing
behaviour (flags, comparison rules, output schema, exit codes) is documented
in the [README](../README.md).

## Pipeline

One invocation is a straight pipeline with no shared state and no persistence:

```text
os.Args, env, stdio, signal context            (cmd/dns-consistency-checker)
        │
        ▼
cli.Run ── parse flags (stdlib flag) ──► config.Input
        │
        ▼
config.Resolve   defaults → config file → env → explicit flags; validation;
                 resolver parsing + de-duplication; PTR conversion;
                 expected values; export pre-checks.   No network I/O.
        │
        ▼
dnsclient.QueryAll   bounded worker pool; one Result per resolver, input order
        │
        ▼
compare.Analyze      pure function: groups, majority, outliers, expected,
                     issues, overall status
        │
        ▼
output.Render (stdout)   output.Export (file)
        │
        ▼
exit code
```

## Packages

| Package | Responsibility | Depends on |
|---------|----------------|------------|
| `cli` | commands, flag parsing, help, logging setup, exit codes | config, dnsclient, compare, output |
| `config` | settings model, limits, precedence, files, validation | dnsclient, normalize |
| `dnsclient` | endpoints, exchange, fallback, retries, pool, CNAME extraction | normalize |
| `normalize` | record registry, canonical values, expected parsing | miekg/dns |
| `compare` | consistency engine | dnsclient, normalize |
| `output` | document schema, table, JSON/YAML, export | compare, dnsclient |
| `testdns` | in-process DNS servers for tests | miekg/dns |

There are no interfaces: every component has exactly one implementation, and
tests use real sockets against local servers instead of mocks.

## DNS exchange

`dnsclient.exchange` performs one query:

1. `context.WithTimeout(ctx, timeout)` — per-exchange deadline.
2. `net.Dialer.DialContext` — UDP sockets are connected, so the kernel drops
   datagrams from other sources.
3. `context.AfterFunc` sets an immediate socket deadline when the context ends
   (timeout or Ctrl-C), unblocking reads at once.
4. `dns.Conn` from miekg/dns handles framing (TCP length prefix) and
   packing; the read buffer is 64 KiB.
5. Replies with a different message ID are skipped on UDP (spoofing / stray
   datagrams) and are a protocol error on TCP.
6. `validate` checks QR, opcode and question section.
7. An unparseable reply with TC=1 is still returned as truncated so the
   caller can fall back to TCP.

`Query` wraps attempts: an attempt is one exchange plus an optional TCP
fallback exchange. TIMEOUT and NETWORK_ERROR are retried after a fixed delay;
nothing else is.

## Concurrency model

```go
sem := make(chan struct{}, concurrency)
for i, r := range resolvers {
    sem <- struct{}{}            // acquire before starting the goroutine
    wg.Go(func() {
        defer func() { <-sem }()
        results[i] = Query(...)  // each goroutine owns one slice index
    })
}
wg.Wait()
```

- At most `concurrency` query goroutines exist at any time.
- No locks: every goroutine writes a distinct index; `wg.Wait` publishes the
  writes to the caller.
- Output order equals input order by construction.
- Every goroutine terminates: all socket I/O has a deadline and cancellation
  sets an immediate one. If the context is cancelled while waiting for a slot,
  remaining resolvers are marked "query canceled" without being started.

## Consistency engine

`compare.Analyze` is a pure, deterministic function of its input. The
comparison key of a usable result is built from its status, CNAME chain and
sorted record values (plus TTLs with `--compare-ttl`), separated by bytes that
cannot occur in presentation values. Groups are sorted by size and key; issues
are emitted in resolver input order followed by group-level issues. Records
within an RRset are sorted by `normalize.CompareValues` (numeric fields and IP
addresses compare numerically, with a byte-order tie-break), which is a total
order, so duplicate removal and output are stable.

## Error model

- Per-resolver problems are data (`Result.Status`, `Result.Error`) and never
  abort a check.
- Command-level problems are `*config.Error` values carrying a `Kind`
  (input, configuration, export) that `cli` maps to exit codes 4, 5 and 6.
  Rendering failures map to 7.
- Findings about a check (including removed duplicate resolvers) are issues
  in the report on stdout. Command errors go to stderr as `error: ...`;
  verbose diagnostics use `log/slog` on stderr.

## Extension points

- **Record type:** add one entry to `normalizers` in
  `internal/normalize/normalize.go` (a function from `dns.RR` to the canonical
  value). Parsing, validation, expected values, comparison and output pick it
  up automatically.
- **Output field:** add it to the document structs in
  `internal/output/document.go`. Additive changes keep `schema_version: 1`;
  renames or removals require incrementing `SchemaVersion`.

## Resource limits

| Setting | Limit |
|---------|-------|
| resolvers per check | 1000 (after de-duplication) |
| timeout | 1ms – 60s |
| retries | 0 – 10 |
| retry delay | 0 – 10s |
| concurrency | 1 – 1000 |
| expected values | 1000 |
| config / resolver / expected file size | 1 MiB |
| CNAME chain length | 16 |

All limits are validated before any query is sent.

## Cross-platform notes

The code uses only portable standard library networking. Behaviour that may
differ by platform:

- A UDP query to a closed port is reported as `NETWORK_ERROR` ("connection
  refused") on Linux, macOS and Windows because the socket is connected and
  receives the ICMP port-unreachable error; on networks that filter ICMP it
  becomes a `TIMEOUT`.
- IPv6 zone identifiers use interface names on Unix (`%en0`) and interface
  indexes on Windows (`%12`).
