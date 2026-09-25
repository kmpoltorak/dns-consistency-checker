# Getting Started

This guide takes you from building the tool to running useful checks. Every
flag and rule is described in detail in the [Reference](REFERENCE.md).

## 1. Install

You need Go 1.27 or newer.

```bash
go install github.com/kmpoltorak/dns-consistency-checker/cmd/dns-consistency-checker@latest
dns-consistency-checker version
```

Or build from source:

```bash
git clone https://github.com/kmpoltorak/dns-consistency-checker.git
cd dns-consistency-checker
make build
./bin/dns-consistency-checker version
```

Or use Docker (optional; the image is a static binary on a distroless,
non-root base):

```bash
make docker-build
docker run --rm dns-consistency-checker:latest check --host example.com --server 1.1.1.1 --server 8.8.8.8
```

The examples below use the short alias `dcc`. Set it once per terminal
session:

```bash
alias dcc=dns-consistency-checker                    # after go install
alias dcc="$PWD/bin/dns-consistency-checker"          # after make build (run in the repository)
```

To keep it permanently, add the alias to your `~/.zshrc` or `~/.bashrc`; for
a source build use the absolute path, e.g.
`alias dcc="$HOME/github/dns-consistency-checker/bin/dns-consistency-checker"`.

## 2. Your first check

Ask for the MX records of `gmail.com` without naming any resolver:

```bash
dcc check --host gmail.com --type MX
echo $?
```

With no resolvers given, the tool uses its 12 built-in public resolvers
(Cloudflare, Google, Quad9, OpenDNS, AdGuard, Control D) and says so in an
`[info]` line. They all return the same five MX records, so the result is
`✅ CONSISTENT` and the exit code is `0`. You will usually also see an
`[info]` line about TTL differences: caching resolvers count TTLs down, so
this is normal and does not affect the result.

To choose resolvers yourself, pass IP addresses or hostnames:

```bash
dcc check --host gmail.com --type MX --server 1.1.1.1 --server google=dns.google
```

A hostname is resolved by your system (the table shows `dns.google (8.8.4.4)`).

## 3. Reading the output

```text
NAME             ADDRESS       STATUS        RESPONSE      TIME
---------------------------------------------------------------
cloudflare       1.1.1.1       ✅ NOERROR    10.20.30.40   18ms
                                             10.20.30.41
internal-dns-1   10.10.10.53   🔵 NOERROR    10.20.30.40   2ms
internal-dns-2   10.10.20.53   🚫 SERVFAIL   -             3ms

✅ agrees   🔵 different answer   🚫 failed

Consistency:
❌ INCONSISTENT
```

- **One row per resolver**, always in the order you gave them. Additional
  records continue on the following lines.
- **✅** the resolver agrees with the majority, **🔵** it answered differently,
  **🚫** it did not return a usable answer (timeout, network error, SERVFAIL,
  REFUSED, …).
- **Consistency** is the verdict: ✅ `CONSISTENT`, ❌ `INCONSISTENT`,
  🟠 `PARTIAL_FAILURE` (answers agree but some resolvers failed) or
  🚫 `TOTAL_FAILURE` (nobody answered).
- **Majority response** and **Response groups** appear when answers differ.
  The majority is only the most common answer; it is not necessarily correct.
- **Issues** explain every finding. Lines marked `[info]` or `[warning]` do
  not change the verdict on their own.

Record order and the letter case of names never matter: `10.0.0.1, 10.0.0.2`
and `10.0.0.2, 10.0.0.1` are the same answer.

## 4. Keep your resolvers in a file

Typing 20 or 50 `--server` flags does not scale. Put them in a file, one per
line, with an optional name:

```text
# resolvers.txt
cloudflare      1.1.1.1
google          8.8.8.8
quad9           9.9.9.9
internal-dns-1  10.10.10.53
internal-dns-2  10.10.20.53:5353
```

```bash
dcc check --host example.com --servers-file resolvers.txt
```

To also store defaults such as the timeout, use a YAML configuration file:

```yaml
# config.yaml
servers:
  - name: cloudflare
    address: 1.1.1.1
  - name: internal-dns-1
    address: 10.10.10.53
defaults:
  timeout: 2s
  retries: 1
```

```bash
dcc check --host example.com --config config.yaml
```

Ready-made examples are in [`testdata/`](../testdata/). Flags override
environment variables, which override the configuration file.

**Make it your default.** Save the YAML file as
`~/.config/dns-consistency-checker/config.yaml` (Windows:
`%AppData%\dns-consistency-checker\config.yaml`) and it is loaded
automatically whenever you run without `--config`:

```bash
mkdir -p ~/.config/dns-consistency-checker
cp config.yaml ~/.config/dns-consistency-checker/config.yaml
dcc check --host example.com        # uses your resolvers and defaults
```

The report notes `[info] loaded default configuration file …` so you always
know which list was used.

## 5. Common tasks

**Verify a change reached every resolver** — compare against the value you
expect:

```bash
dcc check --host dns.google --servers-file resolvers.txt \
  --expected 8.8.8.8 --expected 8.8.4.4
```

Any resolver returning something else makes the check `INCONSISTENT`
(exit code 1). For many values, use `--expected-file` (see
[Expected Result Mode](REFERENCE.md#expected-result-mode)).

**Other record types:**

```bash
dcc check --host _xmpp-server._tcp.gmail.com --type SRV --servers-file resolvers.txt
dcc check --host google.com --type CAA --servers-file resolvers.txt
dcc check --host google.com --type TXT --servers-file resolvers.txt
dcc check --host 8.8.8.8 --type PTR --servers-file resolvers.txt   # IP -> 8.8.8.8.in-addr.arpa.
```

**Use it in a script or CI job** — rely on the exit code and machine-readable
output:

```bash
if dcc check --host api.example.com --servers-file resolvers.txt --output json > result.json; then
  echo "DNS is consistent"
else
  echo "DNS check failed with exit code $?"
fi
jq .summary result.json
```

Exit codes: `0` consistent, `1` inconsistent, `2` partial failure, `3` total
failure, `4`–`7` usage, configuration, export or internal errors.

**Save the full result** while still printing the table:

```bash
dcc check --host example.com --servers-file resolvers.txt --export result.json
```

An existing file is never replaced unless you add `--overwrite`.

**See what happens under the hood** — `-v` adds protocol, attempt and flag
columns and prints diagnostics to stderr:

```bash
dcc check --host gmail.com --type MX --servers-file resolvers.txt -v
```

## 6. Try the failure cases

These commands show each verdict with public resolvers:

| Command | Verdict | Exit |
|---------|---------|------|
| `dcc check --host nonexistent-xyz123.com --server 1.1.1.1 --server 8.8.8.8` | ✅ CONSISTENT (everyone says NXDOMAIN) | 0 |
| `dcc check --host dns.google --server 1.1.1.1 --expected 1.2.3.4` | ❌ INCONSISTENT | 1 |
| `dcc check --host dns.google --server 1.1.1.1 --server 127.0.0.1:1` | 🟠 PARTIAL_FAILURE | 2 |
| `dcc check --host dns.google --server 192.0.2.1 --timeout 500ms --retries 0` | 🚫 TOTAL_FAILURE | 3 |
| `dcc check --host dns.google --server 8.8.8` | error: invalid resolver address | 4 |

`192.0.2.1` is a documentation address that never answers; depending on your
network it is reported as a timeout or as a network error.

## 7. Troubleshooting

- **A CDN-hosted name is `INCONSISTENT`.** Services such as Google or
  Cloudflare return different addresses depending on the resolver's location.
  The tool reports what the resolvers return; the difference is real.
- **`--compare-ttl` is always `INCONSISTENT` on public resolvers.** Cached
  TTLs count down, so they almost never match. Use it for authoritative
  servers.
- **An IPv6 resolver shows 🚫 NETWORK_ERROR.** Your network has no IPv6
  connectivity.
- **A resolver given by hostname shows 🚫 "cannot resolve resolver
  hostname".** Your system's DNS could not resolve that name. Use the IP
  address instead, especially when your own DNS is what you are checking.
- **Unexpected resolvers in the output.** Look for `[info] loaded default
  configuration file` or `[info] no resolvers given` in the Issues section.
- **Icons look misaligned or are missing.** Use a UTF-8 terminal with emoji
  support. JSON and YAML output contain no icons.

Next: the [Reference](REFERENCE.md) covers every flag, the comparison rules
and the JSON/YAML schema.
