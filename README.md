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
internal-dns-1   10.10.10.53   🔵 NOERROR    10.20.30.40   2ms
internal-dns-2   10.10.20.53   🚫 SERVFAIL   -             3ms

✅ agrees   🔵 different answer   🚫 failed

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
- Resolvers as IP addresses or hostnames, from repeated flags, a resolver file,
  a YAML config file, or a per-user default config; 12 popular public
  resolvers built in when you give none.
- Record types A, AAAA, CNAME, MX, NS, TXT, PTR, SRV, CAA, SOA with record-specific normalization.
- CNAME chain following; answers compared at the end of the chain.
- Precise status per resolver: NOERROR, NXDOMAIN, SERVFAIL, REFUSED, FORMERR,
  NOTIMP, TIMEOUT, NETWORK_ERROR, PROTOCOL_ERROR.
- Majority (plurality) response and outlier detection — never labelled "correct".
- TTL differences reported as information, or enforced with `--compare-ttl`.
- Expected-result mode (`--expected`, `--expected-file`).
- Table, JSON and YAML output; JSON/YAML export with overwrite protection.
- Per-attempt timeouts, retries for transient failures, clean Ctrl-C cancellation.
- Deterministic exit codes for scripts and CI.

It is a single static binary: no database, no daemon, no dependencies other
than the resolvers you point it at.

## Quick start

```bash
go install github.com/kmpoltorak/dns-consistency-checker/cmd/dns-consistency-checker@latest

# 12 built-in public resolvers
dns-consistency-checker check --host example.com

# your own resolvers: IP addresses or hostnames
dns-consistency-checker check --host example.com --server 1.1.1.1 --server dns.google
```

With many resolvers, keep them in a file (`NAME ADDRESS` per line) or in
`~/.config/dns-consistency-checker/config.yaml`, which is loaded
automatically:

```bash
dns-consistency-checker check --host example.com --servers-file resolvers.txt
```

Requires Go 1.27+ to build. To build from a clone: `make build`
(→ `bin/dns-consistency-checker`).

## Documentation

| Document | Contents |
|----------|----------|
| [Getting Started](docs/GETTING_STARTED.md) | Step-by-step guide: first check, reading the output, resolver files, common tasks, troubleshooting |
| [Reference](docs/REFERENCE.md) | All flags, configuration, comparison rules, CNAME/TTL handling, expected mode, JSON/YAML schema, export |
| [Architecture](docs/ARCHITECTURE.md) | Design, concurrency and error model, project structure, dependencies, testing, CI |

`dns-consistency-checker help check` prints the full CLI help.

## Consistency and exit codes

Answers are compared as normalized RRsets: record order, letter case of names
and (by default) TTLs do not matter; the response code, CNAME chain and record
data do. See [How Consistency Is Determined](docs/REFERENCE.md#how-consistency-is-determined).

| Exit | Status | Meaning |
|------|--------|---------|
| 0 | ✅ `CONSISTENT` | All resolvers answered and all answers are equivalent |
| 1 | ❌ `INCONSISTENT` | Answers differ, or do not match `--expected` |
| 2 | 🟠 `PARTIAL_FAILURE` | Answers agree, but some resolvers failed |
| 3 | 🚫 `TOTAL_FAILURE` | No resolver returned a usable answer |
| 4 | | Invalid input |
| 5 | | Configuration error |
| 6 | | Export error |
| 7 | | Internal error |

## Development

```bash
make test        # unit and integration tests (no Internet needed)
make test-race   # with the race detector
make lint        # golangci-lint
make vulncheck   # govulncheck
make docker-build
```

CI runs all of the above on Linux, macOS and Windows, plus weekly
vulnerability scans. Details in [Architecture](docs/ARCHITECTURE.md#testing).

## Limitations

- UDP and TCP only; no DNS over TLS, HTTPS or QUIC.
- No DNSSEC validation; the AD flag is reported exactly as the resolver sent it.
- Resolvers given by hostname depend on the system's DNS to find them; use
  IP addresses when that DNS itself is under test.
- Only the answer section is compared; authority/additional data is ignored.
- Case normalization applies to ASCII letters only (as in DNS); internationalized
  names must be given in their ASCII (`xn--`) form.
- EDNS Client Subnet is not sent, so geo-aware answers reflect each resolver's
  own location — this can legitimately produce `INCONSISTENT` for CDN-hosted names.
- Resolver-level retries only; there is no global deadline besides the bounds
  given in [Timeouts](docs/REFERENCE.md#timeouts).

## Roadmap

Possible future additions that stay within the tool's scope:

- DNS over TLS / HTTPS transports.
- Additional record types (HTTPS/SVCB, DS, DNSKEY, TLSA, NAPTR).
- Optional EDNS Client Subnet to compare geo-aware answers.
- Optional DNSSEC validation.

## License

[MIT](LICENSE)
