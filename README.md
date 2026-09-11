# GlobalNetBench

Self-hosted benchmarking for an Internet connection, measured against multiple
world regions. It answers the question a single "speed test" number never can:

> Is this slowdown my local network, my ISP, international transit, or just this
> one destination?

No blockchain software, no mandatory Grafana, no external service required.

Status: **v0.1 (MVP)** — see [`spec.md`](spec.md) for the full design and the
v0.2/v0.3 roadmap.

## What it measures

| Layer | What you get |
| --- | --- |
| Reachability | Can the destination be reached at all, per protocol and per address family |
| Network quality | min/avg/median/p95/p99 RTT, jitter, packet loss |
| Route quality | Traceroute path, hop RTTs, a route fingerprint, and route-change events |
| Application quality | DNS, TCP connect, TLS handshake, time-to-first-byte, total request time |

These are deliberately kept apart rather than collapsed into one number.
Bandwidth testing is intentionally **not** in v0.1.

Everything is measured independently over IPv4 and IPv6, so a broken IPv6
deployment shows up as exactly that instead of dragging every score down.

## Quick start

### Docker (published image)

The fastest path — nothing to clone, SQLite storage, no database to run:

```bash
mkdir globalnetbench && cd globalnetbench
curl -O https://raw.githubusercontent.com/ffaerber/global-net-bench/main/examples/docker-compose.yml
curl -o config.yaml https://raw.githubusercontent.com/ffaerber/global-net-bench/main/config.example.yaml
docker compose up -d
```

Open <http://localhost:8080>. See
[`examples/docker-compose.yml`](examples/docker-compose.yml).

Images are published for `linux/amd64` and `linux/arm64`, so a Raspberry Pi or
an ARM VPS works as well as an x86 server:

| Tag | What it is |
| --- | --- |
| `latest` | The most recent `main` build |
| `0fadf33` | The build of that exact commit |

There are no release version numbers. Every build is tagged with its short
commit SHA, which is also what the binary reports as its version — so whatever
`/api/v1/status` shows can be traced straight back to the source, and pinned:

```bash
GNB_IMAGE=ffaerber/globalnetbench:0fadf33 docker compose up -d
```

Pin a SHA rather than tracking `latest` if you care about reproducibility.

### Docker Compose from source

For PostgreSQL plus the optional Prometheus and Grafana stack:

```bash
git clone https://github.com/ffaerber/global-net-bench.git
cd global-net-bench
cp config.example.yaml config.yaml   # edit to taste
docker compose up -d
```

That compose file uses PostgreSQL, so switch the `database` block in
`config.yaml` to:

```yaml
database:
  type: postgres
  dsn: postgres://globalnetbench:globalnetbench@postgres:5432/globalnetbench?sslmode=disable
```

Prometheus and Grafana are optional and off by default:

```bash
docker compose --profile observability up -d
```

### Running the binary directly

```bash
go build -o globalnetbench ./cmd/globalnetbench
cp config.example.yaml config.yaml
./globalnetbench -config config.yaml
```

With the default SQLite backend there is nothing else to install.

Validate a config without starting anything:

```bash
./globalnetbench -config config.yaml -check
```

## ICMP and traceroute permissions

ICMP and traceroute need raw sockets. The process tries, in order:

1. a **raw** ICMP socket — needs `CAP_NET_RAW` (or root). Required for traceroute.
2. an **unprivileged datagram** ICMP socket — enough for ping, but the kernel
   delivers TTL-exceeded replies to the error queue, so traceroute cannot work.

If neither is available, ICMP tests are skipped and the dashboard says so; TCP,
HTTPS and DNS tests still run and still give you latency. **ICMP failing is never
by itself treated as "host down"** — a target is only reported unreachable when
every protocol fails.

Grant the capability without running as root:

```bash
sudo setcap cap_net_raw+ep ./globalnetbench
```

Or allow unprivileged ping sockets system-wide:

```bash
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

The Docker Compose service drops every capability and adds back only `NET_RAW`.

## How scoring works

Each region gets a 0–100 score built from penalties, not from a magic formula:

| Penalty | Max points |
| --- | --- |
| Packet loss | 45 |
| Latency above the path's own baseline | 25 |
| Jitter | 15 |
| TCP connect failures | 20 |
| HTTP failures | 10 |
| Route instability | 5 |

Two properties matter here:

- **Distance is not a defect.** Latency is compared against what is normal *for
  that path* — a rolling baseline learned from your own history (7 days by
  default), falling back to the region's configured `expected_rtt_ms` until
  enough samples exist. 280 ms to Sydney scores 100 if 280 ms is normal.
- **One broken host does not condemn a region.** With three or more targets the
  region takes the median score; with two, the healthier one wins. This is why
  the example config gives every region two targets.

A target is scored on its best working address family, and IPv4/IPv6 health is
reported separately.

The **Global** score is the weighted mean of regions with data. The **Local /
ISP** score comes from regions marked `local: true`, minus a penalty for failing
DNS resolvers.

## Measurement modes

| Mode | Default interval | What runs |
| --- | --- | --- |
| Health | 60s | ICMP (10 packets), TCP connect, HTTPS timing, DNS |
| Quality | 5m | ICMP burst (50 packets) for jitter and loss |
| Route | 15m | Traceroute and route fingerprinting |
| Full | on demand | Everything, via the dashboard button or the API |

Monitoring is low-overhead by design: probing is rate-limited, concurrency is
capped, and HTTPS responses are truncated (64 KiB by default).

## API

```
GET  /api/v1/status                    scores, capabilities, public IP, uptime
GET  /api/v1/regions                   per-region scores and metrics
GET  /api/v1/targets                   configured targets
GET  /api/v1/measurements              ?region= &target= &protocol= &address_family= &since= &limit=
GET  /api/v1/routes                    ?target= &address_family= &since= &limit=
GET  /api/v1/events                    ?type= &region= &since= &limit=
GET  /api/v1/events/stream             live updates over SSE
POST /api/v1/tests/run                 {"mode":"full","regions":["tokyo"]}
POST /api/v1/tests/run/{region}
GET  /metrics                          Prometheus exposition
```

`since` accepts a duration (`15m`, `24h`) or an RFC 3339 timestamp.

```bash
curl -s localhost:8080/api/v1/status | jq '.snapshot.global_score'
curl -s -X POST localhost:8080/api/v1/tests/run -d '{"mode":"full"}'
```

## Prometheus metrics

`globalnetbench_latency_ms`, `_jitter_ms`, `_packet_loss_ratio`,
`_tcp_connect_ms`, `_tls_handshake_ms`, `_http_ttfb_ms`, `_dns_lookup_ms`,
`_rtt_p95_ms`, `_target_up`, `_region_score`, `_global_score`, `_local_score`,
`_address_family_up`, `_route_changes_total`.

Labels are `site`, `region`, `target`, `protocol`, `address_family` and `port`.
Per-hop IP addresses are deliberately never exported — that cardinality belongs
in the database, not in Prometheus.

## Events

Transitions are recorded, not repeated states, so a target that stays down
produces one event rather than one per cycle:

`connectivity_lost`, `target_recovered`, `packet_loss_spike`, `latency_spike`,
`jitter_spike`, `route_change`, `dns_failure`, `ipv6_failure`,
`public_ip_change`.

Measurements and routes are pruned on a retention schedule. **Events are kept
forever** — they are the record of what actually happened.

## Configuration

See [`config.example.yaml`](config.example.yaml), which documents every key.
The essentials:

```yaml
site:
  id: home          # identifies this vantage point / WAN, for multi-site later
regions:
  - id: asia-japan
    display_name: Tokyo
    latitude: 35.68       # optional; GeoIP places the region when omitted
    longitude: 139.69
    weight: 1.0
    expected_rtt_ms: 200
    targets:
      - id: nrt-01
        hostname: ec2.ap-northeast-1.amazonaws.com
      - id: nrt-02
        hostname: s3.ap-northeast-1.amazonaws.com
```

The example targets are real, regionally located endpoints that answer TCP and
HTTPS. Most cloud endpoints filter ICMP — which is precisely why this tool does
not rely on ping alone. For the most accurate numbers, point the targets at
hosts you control in each region.

Unknown configuration keys are rejected at startup rather than silently ignored.

## The map

The Overview page draws a world map: click a region to open its detail. Each
region appears where you place it with `latitude` and `longitude`, coloured by
its current status, with a great-circle arc drawn from your own position.

You do not have to supply any of those numbers. Where a coordinate is missing,
the map falls back to GeoIP:

| Point | Configured | Fallback with `geoip` enabled |
| --- | --- | --- |
| A region | `latitude` / `longitude` on the region | The address its targets resolve to |
| The origin the arcs start from | The region marked `local: true`, if it has coordinates | Your public egress address |

Configured coordinates always win, and a position that came from GeoIP is drawn
hollow with the reason in its tooltip, the same way an approximate traceroute
hop is. That distinction matters: a database places an address by where its
block is registered, so an anycast endpoint or a recently reassigned range can
land on the wrong continent, and a home connection lands on the ISP's POP rather
than on your street. Where a region really is, only you know — set it explicitly
for anything you care about being right.

Regions are placed from the addresses of their targets, and each region votes:
positions are grouped by city, the largest group wins, and ties go to the
better-graded record. One endpoint whose block resolves to its owner's head
office therefore cannot drag a region across an ocean, and no averaging puts a
dot in the middle of the Atlantic where nothing is. Without `geoip`, a region
with no coordinates is still measured — it is just not plotted, and the
dashboard says how many are missing.

The map is drawn on a canvas from a vendored 1:110m land outline, so it needs no
map tiles, no API key and no Internet access at all. GeoIP lookups are local
file reads; your address is never sent to a geolocation service.

### Replaying a trace

The Routes page replays the most recent trace on a loop: a pulse travels the
path hop by hop, and each leg takes time in proportion to what that hop actually
cost. A slow transatlantic leg visibly drags; a cheap one snaps past. The
readout names the current hop with its incremental and cumulative time.

A traceroute RTT is the round trip from you to that hop, so it is cumulative;
the time attributable to one leg is the difference between consecutive hops.
That difference sometimes comes out negative, because a busy router
deprioritises the ICMP replies it generates itself while still forwarding
traffic normally, so a later hop can report a *lower* RTT than the one before
it. Those legs are clamped to a floor rather than dropped — the hop is still on
the path, it just cannot be timed.

The whole replay is stretched to a few seconds; a real 200 ms trace played at
life speed would be a blink. Relative durations are preserved, so what you are
comparing is still honest. Use **Pause** to stop it; it also stops on its own
when the tab is in the background or you are on another page.

### Traceroute hops on the map

With `geoip` enabled, the Routes page plots the path as well. **There is no GPS
in IP routing**: a traceroute returns addresses, and turning those into
positions is inference from a database. It is right often enough to be useful
and wrong often enough to matter, so the dashboard is explicit about what it
does not know:

| Drawn as | Means |
| --- | --- |
| Solid line | Consecutive hops, both located |
| Dashed line | A gap — hops in between could not be placed, so the path between them is an assumption |
| Filled dot | City-level position |
| Hollow dot | Country-level only; the country is known, the position inside it is not |

Hops on your own LAN, hops behind carrier-grade NAT, and hops that never
answered are not plotted at all, and the caption counts them. Backbone routers
in particular tend to resolve to wherever their address block was registered
rather than where the hardware sits, which is why a path can appear to detour
through a country it never touched.

Enable it by pointing at local database files — nothing about your routes
leaves the machine:

```yaml
geoip:
  enabled: true
  city_db: /data/geoip/dbip-city-lite.mmdb
  asn_db: /data/geoip/dbip-asn-lite.mmdb
```

No database ships with GlobalNetBench. [DB-IP
Lite](https://db-ip.com/db/lite.php) needs no account; MaxMind's GeoLite2 is
more accurate but wants a free licence key. Either works — both are MaxMind-format
`.mmdb`. A configured file that does not exist fails at startup rather than
leaving you with a silently empty map.

## Development

```bash
go test ./...
go vet ./...
go run ./cmd/globalnetbench -config config.yaml
```

Layout:

```
cmd/globalnetbench    entrypoint
internal/config       YAML loading, defaults, validation
internal/probe        ICMP, TCP, HTTPS, DNS, traceroute
internal/engine       test execution, event detection, public IP
internal/score        health scoring and baselines
internal/scheduler    periodic modes and retention
internal/store        SQLite + PostgreSQL persistence
internal/api          REST, SSE, Prometheus
internal/web          embedded dashboard
```

## Docker Hub publishing

[`.github/workflows/docker.yml`](.github/workflows/docker.yml) tests every push
and pull request, then builds and pushes multi-arch images. Pull requests build
the image but never push — a fork PR has no access to the registry credentials.

Configure these in **Settings → Secrets and variables → Actions**:

| Name | Kind | Purpose |
| --- | --- | --- |
| `DOCKERHUB_USERNAME` | Secret | Docker Hub account |
| `DOCKERHUB_TOKEN` | Secret | Docker Hub **access token**, not the password |
| `DOCKERHUB_IMAGE` | Variable (optional) | Overrides the default `<username>/globalnetbench` |

Create the token at Docker Hub → Account Settings → Personal access tokens with
**Read & Write** scope.

Every push to `main` publishes two tags: the short commit SHA and `latest`.
There is no release process and nothing to tag by hand — merging to `main` is
what ships.

The workflow fails early with a clear message if the credentials are missing,
rather than getting as far as a build and then failing at the push.

## Roadmap

- **v0.2** — remote probe agents, iperf-style bandwidth tests, reverse and
  bidirectional testing, alerting. GeoIP/ASN enrichment and the world map have
  landed early; see [The map](#the-map).
- **v0.3** — anomaly detection, route correlation, ISP comparison, multi-WAN and
  multi-site comparison.
