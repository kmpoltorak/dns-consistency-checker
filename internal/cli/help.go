package cli

const mainHelp = `dns-consistency-checker - query one DNS record against many resolvers
concurrently and report whether their answers are consistent.

Usage:
  dns-consistency-checker check [flags]    Run a consistency check
  dns-consistency-checker version          Print the version
  dns-consistency-checker help [command]   Show help

Example:
  dns-consistency-checker check --host example.com --type A \
    --server 1.1.1.1 --server 8.8.8.8 --server 9.9.9.9

Run 'dns-consistency-checker help check' for all flags.

Exit codes:
  0 CONSISTENT        4 INVALID_INPUT
  1 INCONSISTENT      5 CONFIGURATION_ERROR
  2 PARTIAL_FAILURE   6 EXPORT_ERROR
  3 TOTAL_FAILURE     7 INTERNAL_ERROR
`

const checkHelp = `Usage:
  dns-consistency-checker check --host NAME [--type TYPE] RESOLVERS [flags]

Sends the same question to every resolver concurrently, normalizes the
answers into RRsets (record order and letter case of names do not matter)
and compares them.

Query:
  --host NAME             Name to query (required). Accepts FQDNs, SRV-style
                          names (_sip._tcp.example.com) and reverse names.
                          With --type PTR an IP address is converted to its
                          in-addr.arpa / ip6.arpa name automatically.
  --type TYPE             A, AAAA, CNAME, MX, NS, TXT, PTR, SRV, CAA, SOA
                          (default A)

Resolvers (at least one source is required):
  --server ADDR           Resolver address; repeatable. Forms: 8.8.8.8,
                          8.8.8.8:53, 2001:4860:4860::8888,
                          [2001:4860:4860::8888]:53, or NAME=ADDR
                          (e.g. google=8.8.8.8). Default port 53.
  --servers-file PATH     File with one resolver per line: "ADDR" or
                          "NAME ADDR"; blank lines and # comments ignored.
  --config PATH           YAML configuration file (servers and defaults).
                          Its servers are used only when no --server or
                          --servers-file is given.
  Duplicate endpoints (1.1.1.1 and 1.1.1.1:53) are removed with a warning.

Transport:
  --protocol udp|tcp      Transport (default udp)          env DNS_PROTOCOL
  --no-tcp-fallback       Keep truncated UDP answers instead of retrying the
                          query over TCP
  --timeout DURATION      Per-attempt timeout, 1ms-60s (default 3s)
                                                           env DNS_QUERY_TIMEOUT
  --retries N             Extra attempts after a TIMEOUT or NETWORK_ERROR,
                          0-10 (default 1). DNS answers (including SERVFAIL)
                          are never retried.               env DNS_RETRIES
  --retry-delay DURATION  Delay between attempts, 0-10s (default 250ms)
                                                           env DNS_RETRY_DELAY
  --concurrency N         Maximum resolvers queried at once, 1-1000
                          (default 50)                     env DNS_MAX_CONCURRENCY

Comparison:
  --compare-ttl           TTL differences make answers inconsistent (by
                          default they are reported as information only)
  --expected VALUE        Expected record in zone-file RDATA syntax;
                          repeatable. Examples:
                            A      10.20.30.40
                            AAAA   2001:db8::1
                            CNAME  edge.example.net
                            MX     "10 mail.example.com"
                            NS / PTR  ns1.example.com
                            TXT    "v=spf1 -all"   (unquoted = one string;
                                   '"part1" "part2"' = two strings)
                            SRV    "10 60 5060 sip.example.com"
                            CAA    '0 issue "letsencrypt.org"'
                            SOA    "ns1.example.com. hostmaster.example.com.
                                    2024010101 7200 3600 1209600 300"
  --expected-file PATH    YAML file:  records: ["10 mail.example.com", ...]
                          An empty list expects NOERROR with no records.

Output:
  --output table|json|yaml  Format written to stdout (default table)
  --export PATH           Also write the full result to PATH (.json, .yaml,
                          .yml)
  --export-format json|yaml  Export format when it cannot be inferred
  --overwrite             Allow --export to replace an existing file
  -v, --verbose           Diagnostics on stderr (attempts, retries, fallback,
                          timing) and PROTO/ATTEMPTS/FLAGS table columns

Precedence: flags > environment variables > config file > built-in defaults.

Overall status and exit code:
  CONSISTENT       0  every resolver answered and all answers are equivalent
  INCONSISTENT     1  usable answers differ (RRset, NOERROR vs NXDOMAIN,
                      CNAME chain, TTL with --compare-ttl) or do not match
                      --expected
  PARTIAL_FAILURE  2  answers agree but some resolvers failed (TIMEOUT,
                      NETWORK_ERROR, PROTOCOL_ERROR, SERVFAIL, REFUSED,
                      FORMERR, NOTIMP, other RCODEs)
  TOTAL_FAILURE    3  no resolver returned a usable answer
  4 invalid input, 5 configuration error, 6 export error, 7 internal error

Examples:
  dns-consistency-checker check --host example.com --servers-file resolvers.txt
  dns-consistency-checker check --host _sip._tcp.example.com --type SRV \
    --servers-file resolvers.txt --output json | jq .summary
  dns-consistency-checker check --host api.example.com --server 1.1.1.1 \
    --server 8.8.8.8 --expected 10.20.30.40 --expected 10.20.30.41
  dns-consistency-checker check --host 192.0.2.10 --type PTR --config config.yaml
`
