# DNS Consistency Checker

[![CI](https://github.com/kmpoltorak/dns-consistency-checker/actions/workflows/ci.yml/badge.svg)](https://github.com/kmpoltorak/dns-consistency-checker/actions/workflows/ci.yml)

`dns-consistency-checker` queries the same DNS record against many resolvers
concurrently, normalizes the answers, and tells you whether they agree.

```console
$ dns-consistency-checker check --host api.example.com --type A --servers-file resolvers.txt
DNS Consistency Check
=====================

Query:
api.example.com. A

Protocol:
UDP (TCP fallback on truncation)

Resolvers checked: 5

NAME             ADDRESS       STATUS        RESPONSE      TIME
---------------------------------------------------------------
cloudflare       1.1.1.1       ✅ NOERROR    10.20.30.40   18ms
                                             10.20.30.41
google           8.8.8.8       ✅ NOERROR    10.20.30.40   21ms
                                             10.20.30.41
quad9            9.9.9.9       ✅ NOERROR    10.20.30.40   34ms
                                             10.20.30.41
internal-dns-1   10.10.10.53   ❌ NOERROR    10.20.30.40   2ms
internal-dns-2   10.10.20.53   🚫 SERVFAIL   -             3ms

✅ agrees   ❌ different answer   🚫 failed

Consistency:
❌ INCONSISTENT

Successful: 4
Failed: 1

Majority response (3 of 5 resolvers; not necessarily correct):
  10.20.30.40
  10.20.30.41

Response groups:
  Group 1 (3 resolvers):
    10.20.30.40
    10.20.30.41
    resolvers: cloudflare (1.1.1.1), google (8.8.8.8), quad9 (9.9.9.9)
  Group 2 (1 resolver):
    10.20.30.40
    resolvers: internal-dns-1 (10.10.10.53)

Issues:
- internal-dns-1 (10.10.10.53) returned a different RRset than the majority
- internal-dns-2 (10.10.20.53) returned SERVFAIL
```

## Overview

The tool is conceptually `dig` against many servers at once, followed by
record-aware normalization, order-insensitive RRset comparison, grouping,
majority/outlier detection, a clear verdict, deterministic exit codes and
optional JSON/YAML export. It is a single static binary with no database, no
daemon and no network dependencies other than the resolvers you point it at.

## Why this tool exists

After a DNS change, during a migration, or when debugging "works for me"
reports, you want to know whether every resolver your users (or your
services) rely on returns the same data. Doing that by hand with `dig` does
not scale, and naive string comparison of `dig` output produces false alarms:
record order changes, name case differs, TTLs count down. This tool compares
what matters — the normalized RRset — and reports everything else as
information.

## Features

- Concurrent queries with a bounded worker pool (100+ resolvers in one check).
- UDP (default) and TCP; automatic TCP retry when a UDP answer is truncated.
- IPv4 and IPv6 resolvers, custom ports, optional resolver names.
- Resolvers from repeated flags, a resolver file, or a YAML config file.
- Record-specific normalization for A, AAAA, CNAME, MX, NS, TXT, PTR, SRV, CAA, SOA.
- CNAME chain following; answers compared at the end of the chain.
- Precise status per resolver: NOERROR, NXDOMAIN, SERVFAIL, REFUSED, FORMERR,
  NOTIMP, TIMEOUT, NETWORK_ERROR, PROTOCOL_ERROR.
- Majority (plurality) response and outlier detection — never labelled "correct".
- TTL differences reported as information, or enforced with `--compare-ttl`.
- Expected-result mode (`--expected`, `--expected-file`).
- Table, JSON and YAML output; JSON/YAML export with overwrite protection.
- Per-attempt timeouts, retries for transient failures, clean Ctrl-C cancellation.
- Deterministic exit codes for scripts and CI.

## Supported DNS Record Types

| Type | Compared fields | Normalization |
|------|-----------------|---------------|
| A | address | IPv4 dotted quad |
| AAAA | address | RFC 5952 canonical IPv6 text (`2001:db8::1`) |
| CNAME | target | lower-case, fully qualified (trailing dot) |
| NS | name server | lower-case, fully qualified |
| PTR | target | lower-case, fully qualified |
| MX | preference, exchange | exchange lower-case, fully qualified |
| SRV | priority, weight, port, target | target lower-case, fully qualified |
| CAA | flags, tag, value | tag lower-cased (case-insensitive per RFC 8659); value unchanged |
| SOA | mname, rname, serial, refresh, retry, expire, minimum | names lower-case, fully qualified |
| TXT | character strings | each TXT record is one value; string boundaries are preserved (`"a" "b"` ≠ `"ab"`); different TXT records are never merged |

Only ASCII letters are lower-cased; escaped bytes (`\DDD`) are preserved.
Adding a type is one entry in the normalizer registry
(`internal/normalize/normalize.go`).

## How Consistency Is Determined

1. Every resolver result is classified as **usable** (NOERROR, NXDOMAIN) or
   **failed** (TIMEOUT, NETWORK_ERROR, PROTOCOL_ERROR, SERVFAIL, REFUSED,
   FORMERR, NOTIMP and any other RCODE).
2. Usable results are grouped by a comparison key made of:
   - the response code (NOERROR vs NXDOMAIN),
   - the normalized CNAME chain,
   - the sorted set of normalized record values of the final RRset,
   - the TTL of each record — **only** with `--compare-ttl`.

   Not compared: record order, raw text, packet bytes, header flags, the
   authority and additional sections.
3. Groups are ordered by size (largest first), then by key.
4. The **majority response** is the single largest group. If the largest
   groups are tied, there is no majority. The majority is only the most common
   answer; the tool never claims it is correct or authoritative.
5. **Outliers** are resolvers with a usable answer outside the majority group.
6. The overall status:

| Status | Meaning | Exit |
|--------|---------|------|
| `CONSISTENT` | All resolvers answered and all answers are equivalent (e.g. all return the same MX RRset, or all return NXDOMAIN) | 0 |
| `INCONSISTENT` | At least two usable answers differ (different RRsets, NOERROR vs NXDOMAIN, different CNAME chains, TTLs with `--compare-ttl`), or in expected mode a usable answer does not match | 1 |
| `PARTIAL_FAILURE` | All usable answers agree, but at least one resolver failed operationally | 2 |
| `TOTAL_FAILURE` | No resolver returned a usable answer | 3 |

`INCONSISTENT` takes precedence over `PARTIAL_FAILURE`.

## Installation

Requires Go 1.27 or newer. The resulting binary is static and has no runtime
dependencies.

```bash
go install github.com/kmpoltorak/dns-consistency-checker/cmd/dns-consistency-checker@latest
```

## Build from Source

```bash
git clone https://github.com/kmpoltorak/dns-consistency-checker.git
cd dns-consistency-checker
make build          # -> bin/dns-consistency-checker
make test
```

## Quick Start

```bash
dns-consistency-checker check \
  --host example.com \
  --type A \
  --server 1.1.1.1 \
  --server 8.8.8.8 \
  --server 9.9.9.9
echo $?   # 0 = CONSISTENT
```

## CLI Reference

```text
dns-consistency-checker check [flags]
dns-consistency-checker version
dns-consistency-checker help [check]
```

| Flag | Default | Env | Description |
|------|---------|-----|-------------|
| `--host NAME` | — | | Name to query (required). FQDNs, SRV-style names and reverse names are accepted. With `--type PTR` an IP address is converted to its reverse name. |
| `--type TYPE` | `A` | | A, AAAA, CNAME, MX, NS, TXT, PTR, SRV, CAA, SOA (case-insensitive) |
| `--server ADDR` | | | Resolver; repeatable. `ADDR` or `NAME=ADDR` |
| `--servers-file PATH` | | | Resolver file |
| `--config PATH` | | | YAML configuration file |
| `--protocol udp\|tcp` | `udp` | `DNS_PROTOCOL` | Transport |
| `--no-tcp-fallback` | off | | Keep truncated UDP answers instead of retrying over TCP |
| `--timeout DURATION` | `3s` | `DNS_QUERY_TIMEOUT` | Per-attempt timeout (1ms–60s) |
| `--retries N` | `1` | `DNS_RETRIES` | Extra attempts after TIMEOUT / NETWORK_ERROR (0–10) |
| `--retry-delay DURATION` | `250ms` | `DNS_RETRY_DELAY` | Delay between attempts (0–10s) |
| `--concurrency N` | `50` | `DNS_MAX_CONCURRENCY` | Maximum resolvers queried at once (1–1000) |
| `--compare-ttl` | off | | TTL differences make answers inconsistent |
| `--expected VALUE` | | | Expected record value; repeatable |
| `--expected-file PATH` | | | YAML file with expected records |
| `--output table\|json\|yaml` | `table` | | stdout format |
| `--export PATH` | | | Also write the full result to a file |
| `--export-format json\|yaml` | inferred | | Export format if the extension is not `.json`/`.yaml`/`.yml` |
| `--overwrite` | off | | Allow `--export` to replace an existing file |
| `-v`, `--verbose` | off | | Diagnostics on stderr and extra table columns |

Flags accept both `--flag value` and `--flag=value` (single dash also works).
`dns-consistency-checker help check` prints the complete reference.

## Resolver Input

Address forms (default port 53):

```text
8.8.8.8
8.8.8.8:53
2001:4860:4860::8888
[2001:4860:4860::8888]
[2001:4860:4860::8888]:5353
```

Resolvers must be IP literals — a DNS checker should not depend on DNS to
find its resolvers. IPv4-mapped IPv6 addresses (`::ffff:1.1.1.1`) are treated
as IPv4. Link-local IPv6 addresses with a zone (`[fe80::1%en0]:53`) are
accepted.

**Repeated flags** (`NAME=` is optional):

```bash
--server cloudflare=1.1.1.1 --server 8.8.8.8 --server "[2001:4860:4860::8888]:53"
```

**Resolver file** (`--servers-file`): one resolver per line, `ADDRESS` or
`NAME ADDRESS`; blank lines and lines starting with `#` are ignored; whitespace
around entries is trimmed.

```text
# Public DNS
cloudflare 1.1.1.1
8.8.8.8
9.9.9.9

# Internal DNS
internal-dns-1 10.10.10.53
10.10.20.53:5353
```

`--server` and `--servers-file` can be combined; flags come first, then the
file, in order. **Duplicates** (same IP and port, e.g. `1.1.1.1` and
`1.1.1.1:53`) are removed automatically; the first occurrence is kept and each
removed entry is listed as an `[info]` issue (`duplicate_resolver`) in the
report, so it also appears in JSON/YAML output and exports. At most 1000 resolvers per check.

Output always follows input order, regardless of which resolver answers first.

## Configuration File

```yaml
servers:
  - name: cloudflare
    address: 1.1.1.1
  - name: google
    address: 8.8.8.8
  - name: internal-dns-1
    address: 10.10.10.53

defaults:
  timeout: 3s
  retries: 1
  retry_delay: 250ms
  concurrency: 50
  protocol: udp
  tcp_fallback: true
  compare_ttl: false
```

Unknown keys are rejected. All `defaults` keys are optional.

## Configuration Precedence

```text
CLI flags  >  environment variables  >  configuration file  >  built-in defaults
```

Only flags that are given explicitly override lower layers. For the resolver
list, any `--server` or `--servers-file` replaces the configuration file's
`servers` entirely (they are not merged).

Environment variables: `DNS_QUERY_TIMEOUT`, `DNS_RETRIES`, `DNS_RETRY_DELAY`,
`DNS_MAX_CONCURRENCY`, `DNS_PROTOCOL`.

## Timeouts

`--timeout` bounds each exchange (connect, send, receive). A slow resolver
therefore cannot block the check for longer than
`(retries + 1) × timeout + retries × retry-delay`
(doubled per attempt when a truncated UDP answer falls back to TCP, since the
TCP exchange gets its own timeout). Ctrl-C / SIGTERM cancels all in-flight
queries immediately; cancelled resolvers are reported as `NETWORK_ERROR`
("query canceled").

## Retries

- `--retries N` adds up to N attempts, separated by `--retry-delay` (fixed).
- Retried: `TIMEOUT` and `NETWORK_ERROR` — typically transient.
- Never retried: any DNS answer (NOERROR, NXDOMAIN, REFUSED, FORMERR, NOTIMP)
  and `PROTOCOL_ERROR`.
- **SERVFAIL is not retried** on purpose: recursive resolvers cache failures
  (RFC 9520 requires 1–5 minutes), so an immediate retry mostly re-reads the
  cache and only adds latency.
- Each result records the attempt count, total duration (all attempts and
  delays), last error and final status.

## Concurrency

Resolvers are queried by at most `--concurrency` goroutines (default 50). A
semaphore slot is acquired *before* a goroutine starts, so goroutine count is
bounded too. 100 resolvers with default settings complete in roughly one
round of the slowest resolver.

## UDP and TCP

UDP is the default. Queries set RD=1 and AD=1 (RFC 6840 §5.7) and carry an
EDNS(0) OPT record advertising a 1232-byte UDP payload (DNS Flag Day 2020).
UDP sockets are connected, and replies with a non-matching message ID are
ignored while waiting for the real reply. Replies that are not responses,
whose question does not match, or that cannot be parsed are `PROTOCOL_ERROR`.
`--protocol tcp` uses TCP for every query.

## TCP Fallback

When a UDP answer has the TC (truncated) flag, the same question is
automatically re-sent over TCP within the same attempt; the result shows
`protocol_initial: udp`, `protocol_final: tcp`, and an `[info]` issue
(`tcp_fallback`) is added to the report. With `--no-tcp-fallback` the
truncated answer is kept, the `truncated` flag is reported, and a warning
issue says the RRset may be incomplete.

## TTL Comparison

TTLs are collected for every record and included in JSON/YAML output. By
default they do not affect consistency — caching resolvers count TTLs down,
so differences are normal. Differences inside a response group are reported
as one `[info]` issue per group. With `--compare-ttl`, TTLs become part of the
comparison key and any difference is `INCONSISTENT`.

## CNAME Handling

For every type except CNAME, the tool follows CNAME records in the answer
section starting from the query name (loop-safe, at most 16 hops):

```text
www.example.com
  -> frontend.example.net
  -> edge.example.net
  -> 10.20.30.40
```

Each result contains `cname_chain` (`frontend.example.net.`,
`edge.example.net.`), `final_name` (`edge.example.net.`) and the records of the
queried type owned by the final name. Both the chain and the final RRset are
compared. Answer records that are not on the chain are not compared; they are
counted in `ignored_records` and reported as an `[info]` issue, and the authority/additional sections are
never compared, so resolvers adding different glue do not cause false alarms.
For `--type CNAME`, no chain is followed: the RRset is the CNAME owned by the
query name.

## Expected Result Mode

Verify the answers against known data:

```bash
dns-consistency-checker check --host api.example.com --type A \
  --servers-file resolvers.txt \
  --expected 10.20.30.40 --expected 10.20.30.41
```

```text
Expected RRset:
  10.20.30.40
  10.20.30.41

Matching resolvers: 4
Non-matching resolvers: 1
Failed resolvers: 1
```

A resolver matches when it returns NOERROR and exactly the expected set of
values (TTLs and CNAME chain are not part of the match). NXDOMAIN is
non-matching. Any non-matching usable answer makes the check `INCONSISTENT`.
Expected values are normalized by exactly the same code as resolver answers
(they are parsed as zone-file RDATA and round-tripped through the DNS wire
format), so `MAIL.Example.com` matches `mail.example.com.`.

Value syntax (zone-file RDATA; names may omit the trailing dot):

| Type | Example |
|------|---------|
| A | `--expected 10.20.30.40` |
| AAAA | `--expected 2001:db8::1` |
| CNAME | `--expected edge.example.net` |
| NS | `--expected ns1.example.com` |
| PTR | `--expected host.example.com` |
| MX | `--expected "10 mail.example.com"` |
| SRV | `--expected "10 60 5060 sip.example.com"` (priority weight port target) |
| CAA | `--expected '0 issue "letsencrypt.org"'` |
| SOA | `--expected "ns1.example.com. hostmaster.example.com. 2024010101 7200 3600 1209600 300"` |
| TXT | `--expected "v=spf1 -all"` (unquoted: one string) or `--expected '"part1" "part2"'` (quoted: multiple strings) |

For many values, use a YAML file:

```yaml
# expected-mx.yaml
records:
  - 10 mail1.example.com.
  - 20 mail2.example.com.
```

```bash
dns-consistency-checker check --host example.com --type MX \
  --servers-file resolvers.txt --expected-file expected-mx.yaml
```

`--expected` and `--expected-file` values are combined. An expected file with
`records: []` expects NOERROR with no records (NODATA).

## Output Formats

`--output table` (default) is for humans. `--output json` and `--output yaml`
print the full structured result. Results (including all issues) always go to
**stdout**; errors and verbose diagnostics go to **stderr**, so piping works:

```bash
dns-consistency-checker check --host example.com --servers-file resolvers.txt --output json | jq .summary.status
```

In the table, each resolver's status is marked ✅ (agrees with the majority
and, in expected mode, matches the expected RRset), ❌ (usable answer that
differs from the majority or the expected RRset, or no majority exists) or 🚫
(failed). The overall status is marked ✅ CONSISTENT, ❌ INCONSISTENT,
⚠️ PARTIAL_FAILURE or 🚫 TOTAL_FAILURE. The icons need a UTF-8 terminal with
emoji support (any modern terminal, including Windows Terminal); JSON and YAML
output contain no icons.

`--verbose` adds PROTO (`udp`, `tcp`, `udp->tcp`), ATTEMPTS and FLAGS
(`aa tc rd ra ad cd` as reported by the resolver) columns. The AD flag is
shown as received; this tool does not perform DNSSEC validation.

## JSON Output

Schema version 1. Field names and structure are stable within a schema
version; arrays are always present (possibly empty); `expected` appears only
in expected mode and `error` only on failed results; ordering is deterministic
(results in input order, groups by size then key, answers sorted).

```json
{
  "schema_version": 1,
  "tool": { "name": "dns-consistency-checker", "version": "v1.0.0" },
  "query": { "name": "example.com.", "type": "A", "protocol": "udp", "tcp_fallback": true,
             "compare_ttl": false, "timeout_ms": 3000, "retries": 1 },
  "summary": { "status": "PARTIAL_FAILURE", "resolvers_total": 2, "successful": 1, "failed": 1,
               "groups": 1, "majority_group": 1, "outliers": [] },
  "groups": [
    { "id": 1, "status": "NOERROR", "cname_chain": [], "answers": ["93.184.216.34"],
      "resolvers": ["cloudflare (1.1.1.1)"], "count": 1 }
  ],
  "results": [
    {
      "resolver": { "name": "cloudflare", "address": "1.1.1.1:53" },
      "query_name": "example.com.", "query_type": "A",
      "status": "NOERROR", "protocol_initial": "udp", "protocol_final": "udp",
      "duration_ms": 18, "attempts": 1, "timestamp": "2026-09-24T20:00:00.123Z",
      "flags": { "authoritative": false, "truncated": false, "recursion_desired": true,
                 "recursion_available": true, "authenticated_data": false, "checking_disabled": false },
      "cname_chain": [], "final_name": "example.com.",
      "answers": [ { "name": "example.com.", "type": "A", "value": "93.184.216.34", "ttl": 300,
                     "raw": "example.com.\t300\tIN\tA\t93.184.216.34" } ],
      "ignored_records": 0, "group": 1
    },
    {
      "resolver": { "name": "internal", "address": "10.0.0.53:53" },
      "query_name": "example.com.", "query_type": "A",
      "status": "TIMEOUT", "protocol_initial": "udp", "protocol_final": "udp",
      "duration_ms": 6250, "attempts": 2, "timestamp": "2026-09-24T20:00:00.123Z",
      "flags": { "authoritative": false, "truncated": false, "recursion_desired": false,
                 "recursion_available": false, "authenticated_data": false, "checking_disabled": false },
      "cname_chain": [], "final_name": "example.com.", "answers": [],
      "ignored_records": 0, "group": 0,
      "error": { "category": "timeout", "message": "no response within 3s" }
    }
  ],
  "issues": [
    { "resolver": "internal (10.0.0.53)", "type": "timeout", "severity": "error",
      "message": "internal (10.0.0.53) timed out: no response within 3s (2 attempts)" }
  ]
}
```

Key fields:

| Field | Notes |
|-------|-------|
| `summary.majority_group` | ID of the majority group, `0` if there is none (tie) |
| `summary.outliers` | Resolvers with usable answers outside the majority |
| `results[].resolver.address` | Always `IP:PORT` / `[IPv6]:PORT` |
| `results[].answers[].value` | Normalized value (comparison key); `raw` is the original presentation |
| `results[].group` | Group ID, `0` for failed results |
| `results[].ignored_records` | Answer records not compared (outside the CNAME chain or of another type) |
| `results[].error.category` | Lower-case status: `timeout`, `network_error`, `protocol_error`, `servfail`, `refused`, `formerr`, `notimp`, … |
| `issues[].type` | `timeout`, `network_error`, `protocol_error`, `servfail`, `refused`, `formerr`, `notimp`, `rcode_error`, `different_rcode`, `different_rrset`, `truncated`, `expected_mismatch`, `no_majority`, `ttl_difference`, `duplicate_resolver`, `tcp_fallback`, `ignored_records` |
| `issues[].severity` | `error`, `warning`, `info` |
| `expected` | `records`, `matching`, `non_matching`, `failed`, and the three resolver lists |

## YAML Output

`--output yaml` has exactly the same structure and field names as JSON:

```yaml
schema_version: 1
query:
  name: api.example.com.
  type: A
summary:
  status: INCONSISTENT
  resolvers_total: 5
  successful: 4
  failed: 1
groups:
  - id: 1
    answers:
      - 10.20.30.40
      - 10.20.30.41
    resolvers:
      - cloudflare (1.1.1.1)
      - google (8.8.8.8)
      - quad9 (9.9.9.9)
```

## Exporting Results

```bash
--export result.json                       # JSON, inferred from extension
--export result.yaml                       # YAML (.yaml or .yml)
--export result.out --export-format yaml   # explicit format
```

Export always contains the full document, independent of `--output`. An
existing file is never replaced unless `--overwrite` is given; this is checked
before any query is sent and enforced atomically when writing (`O_EXCL`).
Files are created with mode 0644. No temporary files are used.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | `CONSISTENT` (also `help`, `version`) |
| 1 | `INCONSISTENT` |
| 2 | `PARTIAL_FAILURE` |
| 3 | `TOTAL_FAILURE` |
| 4 | `INVALID_INPUT` — bad flags, host, type, resolvers, resolver file, expected values/file |
| 5 | `CONFIGURATION_ERROR` — configuration file or environment variables |
| 6 | `EXPORT_ERROR` — unsupported export format, existing file without `--overwrite`, write failure |
| 7 | `INTERNAL_ERROR` — failure writing stdout or unexpected errors |

```bash
if dns-consistency-checker check --host api.example.com --servers-file resolvers.txt --output json > result.json; then
  echo "DNS is consistent"
else
  echo "check failed with exit code $?"
fi
```

## Examples

Basic check:

```bash
dns-consistency-checker check --host example.com --type A --server 1.1.1.1 --server 8.8.8.8
```

Resolver file:

```bash
dns-consistency-checker check --host example.com --type AAAA --servers-file resolvers.txt
```

MX:

```bash
dns-consistency-checker check --host example.com --type MX --servers-file resolvers.txt
```

SRV:

```bash
dns-consistency-checker check --host _sip._tcp.example.com --type SRV --servers-file resolvers.txt
```

TCP:

```bash
dns-consistency-checker check --host example.com --type TXT --protocol tcp --servers-file resolvers.txt
```

JSON / YAML on stdout:

```bash
dns-consistency-checker check --host example.com --type A --servers-file resolvers.txt --output json
dns-consistency-checker check --host example.com --type A --servers-file resolvers.txt --output yaml
```

Export:

```bash
dns-consistency-checker check --host example.com --type A --servers-file resolvers.txt --export result.json
dns-consistency-checker check --host example.com --type A --servers-file resolvers.txt --export result.yaml
```

TTL comparison:

```bash
dns-consistency-checker check --host example.com --type A --servers-file resolvers.txt --compare-ttl
```

Reverse lookup (IP converted to `4.3.2.1.in-addr.arpa.`; IPv6 to `ip6.arpa.`):

```bash
dns-consistency-checker check --host 1.2.3.4 --type PTR --servers-file resolvers.txt
```

Configuration file with environment override:

```bash
DNS_QUERY_TIMEOUT=1s dns-consistency-checker check --host example.com --config config.yaml
```

Example files live in [`testdata/`](testdata/).

## Testing

```bash
make test              # all unit and integration tests
make test-race         # with the race detector
make test-integration  # CLI end-to-end scenarios only
make lint              # golangci-lint
go test -bench . -run '^$' ./internal/normalize ./internal/compare   # benchmarks
```

Tests never use the Internet. `internal/testdns` starts deterministic
in-process DNS servers on ephemeral localhost ports (UDP and TCP on the same
port) that simulate every response code, timeouts, truncation, TCP-only
servers, reordered and duplicated records, CNAME chains and loops, TTL
differences and all supported record types. Network errors use closed ports;
protocol errors use a raw UDP socket that sends garbage, short messages and
wrong message IDs. IPv6 tests are skipped automatically where `::1` is
unavailable.

## Architecture

```text
flags + env + config ─► config.Resolve ─► dnsclient.QueryAll ─► compare.Analyze ─► output.Render / output.Export ─► exit code
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design, concurrency
model, error model and extension points.

## Project Structure

```text
cmd/dns-consistency-checker/   main: signals, stdio, exit code
internal/cli/                  commands, flags, help, orchestration, exit codes; end-to-end tests
internal/config/               defaults, limits, env, YAML config, resolver/expected files, validation
internal/dnsclient/            resolver parsing, UDP/TCP exchange, TCP fallback, retries, worker pool
internal/normalize/            record type registry, normalization, expected-value parsing
internal/compare/              consistency engine: groups, majority, outliers, TTL, expected, issues, status
internal/output/               JSON/YAML schema, table rendering, export
internal/testdns/              local DNS test server (tests only)
testdata/                      example resolver list, config, expected file
docs/                          architecture documentation
```

Dependencies:

| Module | Why |
|--------|-----|
| [`github.com/miekg/dns`](https://github.com/miekg/dns) | The de-facto standard Go DNS library (used by CoreDNS). Correct wire format for all record types and TCP framing; avoids reimplementing the DNS protocol. |
| [`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml) | YAML configuration and output; the maintained continuation of `gopkg.in/yaml.v3`. |

Everything else is the Go standard library.

## Docker

The Docker image is optional; the native binary is the primary distribution.

```bash
make docker-build
docker run --rm dns-consistency-checker:dev check --host example.com --server 1.1.1.1 --server 8.8.8.8
```

Multi-stage build, static binary on `gcr.io/distroless/static-debian12:nonroot`:
no shell, no compiler, runs as a non-root user.

## CI

GitHub Actions ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs
gofmt verification, `go vet`, `go build` and `go test` on Linux, macOS and
Windows, `go test -race` on Linux, golangci-lint, and a Docker build with a
smoke test. No step needs Internet DNS resolvers.

## Limitations

- UDP and TCP only; no DNS over TLS, HTTPS or QUIC.
- No DNSSEC validation; the AD flag is reported exactly as the resolver sent it.
- Resolvers must be IP addresses, not hostnames.
- Only the answer section is compared; authority/additional data is ignored.
- Case normalization applies to ASCII letters only (as in DNS); internationalized
  names must be given in their ASCII (`xn--`) form.
- EDNS Client Subnet is not sent, so geo-aware answers reflect each resolver's
  own location — this can legitimately produce `INCONSISTENT` for CDN-hosted names.
- Resolver-level retries only; there is no global deadline besides the bounds
  given in [Timeouts](#timeouts).

## Roadmap

Possible future additions that stay within the tool's scope:

- DNS over TLS / HTTPS transports.
- Additional record types (HTTPS/SVCB, DS, DNSKEY, TLSA, NAPTR).
- Optional EDNS Client Subnet to compare geo-aware answers.
- Optional DNSSEC validation.

## License

[MIT](LICENSE)
