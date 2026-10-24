# DNS Consistency Checker

[![CI](https://github.com/kmpoltorak/dns-consistency-checker/actions/workflows/ci.yml/badge.svg)](https://github.com/kmpoltorak/dns-consistency-checker/actions/workflows/ci.yml)

`dns-consistency-checker` sends the same DNS question to many resolvers at once,
normalizes the answers and tells you whether they agree. Use it after a DNS
change, during a migration, or when one user's resolver sees something the rest
do not. It compares RRsets, so record order, name case and counting-down TTLs
do not cause false alarms.

```console
$ dns-consistency-checker check --host example.com --type A --server 1.1.1.1 --server 8.8.8.8 --server 9.9.9.9
DNS Consistency Check
=====================

Query:
example.com. A

Protocol:
UDP (TCP fallback on truncation)

Resolvers checked: 3

SERVER    STATUS       RESPONSE         TIME
--------------------------------------------
1.1.1.1   ✅ NOERROR   104.20.23.154    22ms
                       172.66.147.243
8.8.8.8   ✅ NOERROR   104.20.23.154    36ms
                       172.66.147.243
9.9.9.9   ✅ NOERROR   104.20.23.154    14ms
                       172.66.147.243

✅ agrees   🔵 different answer   🚫 failed

Consistency:
✅ CONSISTENT

Successful: 3
Failed: 0

Issues:
- [info] TTL values differ within response group 1: TTL 104-300s on 2 of 2 records (per-record TTLs are in the structured output)
```

- Each resolver gets a precise status (NOERROR, NXDOMAIN, SERVFAIL, REFUSED, FORMERR, NOTIMP, TIMEOUT, NETWORK_ERROR, PROTOCOL_ERROR), and the exit code tells scripts which of consistent, inconsistent, partial or total failure happened.
- The majority answer is reported as a majority, never as "correct".
- Only timeouts and network errors are retried; a DNS answer, SERVFAIL included, never is. Truncated UDP answers are retried over TCP.
- `--export` refuses to replace an existing file without `--overwrite`.
- Ctrl-C cancels cleanly.

## Install

```bash
go install github.com/kmpoltorak/dns-consistency-checker/cmd/dns-consistency-checker@latest
```

Or from a clone with Go 1.27+: `make build` (→ `bin/dns-consistency-checker`).
It is a single static binary with no other dependencies.

## Quick start

```bash
# 12 built-in public resolvers (Cloudflare, Google, Quad9, OpenDNS, AdGuard, Control D)
dns-consistency-checker check --host example.com

# your own resolvers: IP addresses or hostnames, optionally named
dns-consistency-checker check --host example.com --server 1.1.1.1 --server google=dns.google

# many resolvers: one "ADDR" or "NAME ADDR" per line
dns-consistency-checker check --host example.com --servers-file resolvers.txt

# check against what the record should be
dns-consistency-checker check --host example.com --type MX --expected "10 mail.example.com"
```

`~/.config/dns-consistency-checker/config.yaml` is loaded automatically when it
exists. [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) walks through reading
the output and common tasks.

## Usage

`dns-consistency-checker help check` prints every flag. Precedence: flags >
environment variables > config file > built-in defaults.

| Option | Meaning |
|---|---|
| `--host NAME` | name to query (required); with `--type PTR` an IP is converted to its reverse name |
| `--type TYPE` | A, AAAA, CNAME, MX, NS, TXT, PTR, SRV, CAA, SOA (default A) |
| `--server ADDR` | resolver, repeatable: `8.8.8.8`, `[2001:4860:4860::8888]:53`, `dns.google`, `NAME=ADDR` |
| `--servers-file PATH`, `--config PATH` | resolvers from a file or YAML config |
| `--protocol udp\|tcp` | transport (default udp); `--no-tcp-fallback` keeps truncated answers |
| `--timeout`, `--retries`, `--retry-delay` | per attempt, default 3s, 1 retry, 250ms |
| `--concurrency N` | resolvers queried at once (default 50) |
| `--compare-ttl` | TTL differences make answers inconsistent (default: information only) |
| `--expected VALUE`, `--expected-file PATH` | expected records in zone-file RDATA syntax |
| `--output table\|json\|yaml` | stdout format |
| `--export PATH` | also write the full result to `.json` / `.yaml` |
| `-v` | diagnostics on stderr and extra table columns |

Environment variables: `DNS_PROTOCOL`, `DNS_QUERY_TIMEOUT`, `DNS_RETRIES`,
`DNS_RETRY_DELAY`, `DNS_MAX_CONCURRENCY`.

Answers are compared as normalized RRsets: the response code, CNAME chain (the
answer at its end) and record data matter; order, name case and, by default,
TTLs do not. Comparison rules, the YAML config and the JSON/YAML schema are in
[docs/REFERENCE.md](docs/REFERENCE.md).

| Exit | Status | Meaning |
|---|---|---|
| 0 | `CONSISTENT` | all resolvers answered and the answers are equivalent |
| 1 | `INCONSISTENT` | answers differ, or do not match `--expected` |
| 2 | `PARTIAL_FAILURE` | answers agree, but some resolvers failed |
| 3 | `TOTAL_FAILURE` | no resolver returned a usable answer |
| 4 | | invalid input |
| 5 | | configuration error |
| 6 | | export error |
| 7 | | internal error |

## Limitations

- UDP and TCP only: no DNS over TLS, HTTPS or QUIC.
- No DNSSEC validation; the AD flag is reported as the resolver sent it.
- Record types beyond the ten listed (HTTPS/SVCB, DS, DNSKEY, TLSA, NAPTR) are not supported.
- Resolvers given by hostname are found with the system's DNS; use IP addresses when that DNS is under test.
- Only the answer section is compared; authority and additional data are ignored.
- Case normalization covers ASCII only: give internationalized names in `xn--` form.
- EDNS Client Subnet is not sent, so CDN-hosted names can legitimately come out `INCONSISTENT`.
- No global deadline beyond the per-attempt bounds in [Timeouts](docs/REFERENCE.md#timeouts).

## Development

```bash
make test               # unit and integration tests, no Internet needed
make test-race          # with the race detector
make test-integration   # only the end-to-end CLI tests
make lint               # golangci-lint
make vulncheck          # govulncheck
make docker-build
```

Integration tests run the whole CLI against DNS servers started in-process
(`internal/testdns`), so they need no network and can fake any response code,
truncation or timeout. CI runs the tests on Linux, macOS and Windows, plus a
weekly vulnerability scan. Design notes: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

Built with an AI coding assistant. I wrote the spec and reviewed every change.

## License

MIT, see [LICENSE](LICENSE).
