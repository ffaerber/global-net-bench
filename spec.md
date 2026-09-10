Global Internet Connection Benchmark — Application Specification

1. Purpose

Build a self-hosted application that measures the quality of an Internet connection across multiple world regions.

The application must not depend on any blockchain software. It should answer questions such as:

Is my Internet connection healthy locally?

How good is my routing to Europe, Africa, Asia, North America, South America, and Oceania?

Is a slowdown caused by my ISP/local connection or by international routing?

Is packet loss or jitter affecting specific regions?

Did the route to a region change?

Is IPv4 behaving differently from IPv6?

How stable is the connection over hours, days, and weeks?

What is the actual usable upload/download performance to distant regions?

The software should be suitable for home labs, servers, validators, gaming, VoIP, remote work, and general ISP benchmarking.

2. Working Name

GlobalNetBench

Alternative names:

NetScope

RouteBench

WorldPing

NetAtlas

GlobalLink

The codebase should avoid blockchain-specific terminology.

3. Primary Design Principles

Region-aware

Tests must cover configurable locations around the world.

Protocol-aware

Do not rely on ICMP ping alone.

Support ICMP, TCP, HTTP/HTTPS, DNS, and optionally QUIC/UDP.

Low-overhead by default

Continuous monitoring must not consume large amounts of bandwidth.

Full bandwidth tests run less frequently.

Historical

Store measurements over time for comparison and anomaly detection.

Self-hostable

Entire controller/dashboard stack should run with Docker Compose.

Probe-independent

Core application must work with public Internet targets.

Optional remote probe agents provide more accurate two-way testing.

IPv4 + IPv6

Measure both independently whenever available.

Safe

Never flood or aggressively probe arbitrary Internet hosts.

Rate-limit tests and use configured/approved targets.

4. High-Level Architecture

                      ┌──────────────────────┐
                      │   GlobalNetBench UI  │
                      └──────────┬───────────┘
                                 │
                      ┌──────────▼───────────┐
                      │     Controller       │
                      │ scheduler + API      │
                      └─────┬─────────┬──────┘
                            │         │
                 ┌──────────▼───┐ ┌──▼──────────────┐
                 │ Test Engine  │ │ Metrics / DB    │
                 └──────┬───────┘ └─────────────────┘
                        │
       ┌────────────────┼───────────────────┐
       │                │                   │
┌──────▼─────┐   ┌──────▼──────┐    ┌──────▼──────┐
│ Public     │   │ User-owned  │    │ Cloud / CDN │
│ endpoints  │   │ probe agent │    │ test target │
└────────────┘   └─────────────┘    └─────────────┘

5. Main Components

5.1 Controller

Responsibilities:

load configuration

schedule tests

select regional targets

execute tests

normalize results

calculate health scores

expose REST API

export Prometheus metrics

trigger alerts

store historical measurements

Recommended implementation:

Go

single static binary

Docker image

REST + WebSocket/SSE for live measurements

5.2 Test Engine

The test engine must support several independent test types.

ICMP latency

Measure:

minimum RTT

average RTT

median RTT

p95 RTT

p99 RTT

packet loss

jitter

Example:

Tokyo
avg RTT:    192 ms
p95 RTT:    205 ms
packet loss: 0.2 %
jitter:       7 ms

ICMP failure must not automatically mean that the target is unreachable.

5.3 TCP Connection Test

Measure TCP connection establishment time.

Default ports:

443

configurable additional ports

Metrics:

TCP connect RTT

timeout rate

failure rate

This provides a useful fallback when ICMP is filtered.

5.4 HTTP/HTTPS Test

Perform a small HTTP request against a configured endpoint.

Measure:

DNS resolution time

TCP connect time

TLS handshake time

time to first byte

total request time

HTTP status

response size

Example:

DNS       12 ms
TCP      104 ms
TLS      118 ms
TTFB     231 ms
Total    240 ms

5.5 DNS Benchmark

Test configurable DNS resolvers.

Examples:

ISP DNS

Cloudflare

Google

Quad9

user-defined resolver

Measure:

lookup latency

timeout rate

SERVFAIL rate

IPv4 vs IPv6

cached vs uncached lookup when possible

5.6 Route Analysis

Use:

traceroute

tracepath

MTR

Capture:

hop count

per-hop RTT

route changes

packet loss observations

ASN changes

country changes when GeoIP data is available

The application should calculate a route fingerprint.

Example:

Cyprus
  ↓
Cyta
  ↓
Athens
  ↓
Frankfurt
  ↓
NTT
  ↓
Tokyo

If the route changes significantly, store an event.

5.7 Bandwidth Tests

Bandwidth testing must be separate from lightweight monitoring.

Support:

download throughput

upload throughput

simultaneous bidirectional test

TCP streams

optional parallel streams

Possible backends:

iperf3

dedicated GlobalNetBench probe protocol

HTTP download/upload endpoint

Default schedule:

lightweight tests every 1–5 minutes

bandwidth tests every 1–6 hours

Bandwidth tests must have configurable data limits.

Example:

bandwidth:
  enabled: true
  interval: 3h
  max_download_mb: 100
  max_upload_mb: 50

5.8 Jitter and Packet-Loss Test

Run a longer low-bandwidth test where appropriate.

Metrics:

average jitter

p95 jitter

packet loss

burst loss

out-of-order packets where measurable

Useful for:

voice/video

gaming

realtime systems

distributed applications

6. Regional Testing

The user should configure logical regions rather than only individual servers.

Initial suggested regions:

Cyprus / local
Central Europe
Northern Europe
United Kingdom
North America East
North America West
South America
Southern Africa
East Asia
Southeast Asia
Australia
Middle East

Example target set:

regions:

  - id: local
    name: Cyprus
    targets:
      - type: https
        host: example.cy

  - id: eu-central
    name: Frankfurt
    targets:
      - type: probe
        host: fra1.example.net

  - id: eu-north
    name: Helsinki
    targets:
      - type: probe
        host: hel1.example.net

  - id: africa
    name: Johannesburg
    targets:
      - type: probe
        host: jnb1.example.net

  - id: asia-japan
    name: Tokyo
    targets:
      - type: probe
        host: nrt1.example.net

  - id: south-america
    name: São Paulo
    targets:
      - type: probe
        host: gru1.example.net

Each region should support multiple targets.

The benchmark should preferably use at least 2 targets per region so that one broken server does not make an entire region look unhealthy.

7. Remote Probe Agent

An optional lightweight probe agent should be provided.

The agent can run on inexpensive VPS instances.

Example locations:

Frankfurt
Helsinki
London
New York
Los Angeles
São Paulo
Johannesburg
Tokyo
Singapore
Sydney

The probe agent enables:

controlled ping targets

TCP tests

HTTP testing

iperf-style transfer testing

reverse testing

bidirectional testing

Architecture:

Home / Server in Cyprus
       │
       ├────► Tokyo probe
       ├────► Helsinki probe
       ├────► Johannesburg probe
       └────► São Paulo probe

Optional reverse mode:

Tokyo probe ─────► Cyprus
Helsinki probe ──► Cyprus
São Paulo probe ─► Cyprus

This is important because Internet routing can be asymmetric.

8. Probe Agent Security

Probe communication must support:

TLS

token authentication

optional mutual TLS

configurable IP allow-list

rate limits

bandwidth limits

The probe must not become a public open speed-test server unless explicitly configured.

9. Measurement Modes

Continuous Health Test

Low bandwidth.

Default every 60 seconds:

ICMP

TCP 443

DNS

small HTTPS request

Route Test

Default every 15 minutes:

traceroute/MTR

route fingerprint

Quality Test

Default every 5 minutes:

packet loss

jitter

latency distribution

Bandwidth Test

Default every 3 hours:

download throughput

upload throughput

Manual Full Test

User presses:

RUN GLOBAL BENCHMARK

All configured regions are tested immediately.

10. Health Scores

The application should expose several scores instead of one opaque number.

Regional Connectivity Score

0–100 based on:

packet loss

latency relative to historical baseline

jitter

TCP connection success

HTTP success

route stability

Local ISP Score

Measures:

gateway/local latency

first ISP hops

DNS

packet loss close to the user

Global Connectivity Score

Weighted aggregate of all configured regions.

Example:

Global Connectivity       93 / 100

Cyprus                    99
Central Europe            98
Northern Europe           94
North America             90
South America             82
Southern Africa           88
East Asia                 91
Australia                 84

Absolute latency must not be treated as inherently bad merely because a destination is physically distant.

The score should compare a region against:

physical expectations

user's historical baseline

packet loss/jitter

reachability

11. Baseline Learning

The system should automatically learn normal behavior.

For each target/region store rolling baselines for:

24 hours

7 days

30 days

Example:

Tokyo normal RTT:
185–205 ms

Current:
267 ms

Status:
DEGRADED

This is more useful than hard-coded universal thresholds.

12. Event Detection

Generate events for:

connectivity lost

regional outage

packet loss spike

latency spike

jitter spike

bandwidth collapse

DNS failure

IPv6 failure

major route change

ISP/ASN route change

public IP change

Example:

09:41  Tokyo latency increased 43%
09:42  Route changed through Singapore
09:43  Packet loss increased to 4.8%
10:07  Route returned to normal

13. Correlation

The UI should correlate different observations.

Example:

                    09:00  10:00  11:00

Local latency         ────────
Europe latency        ────────
Tokyo latency         ───╱^^^^
Tokyo packet loss     ───╱^^^^
Tokyo route changed      ▲

This makes it possible to distinguish:

Local ISP problem
vs
International transit problem
vs
Single destination problem

14. World Map

Dashboard should include a world map showing each configured region.

Example:

        Helsinki ● 72 ms
                \
Cyprus ●───────── Frankfurt ● 48 ms
   │
   ├──────────── Johannesburg ● 142 ms
   │
   ├──────────────── Tokyo ● 194 ms
   │
   └──────────────────────── São Paulo ● 238 ms

Each point shows:

latency

packet loss

status

bandwidth

last successful test

Clicking a region opens its history.

15. Dashboard Pages

Overview

Show:

current public IP

ISP

ASN

IPv4 status

IPv6 status

Global Connectivity Score

Internet uptime

current download/upload

regional health map

Regions

Table:

Region

RTT

Jitter

Loss

Download

Upload

Status

Frankfurt

48 ms

2 ms

0%

920 Mbps

490 Mbps

Excellent

Helsinki

72 ms

4 ms

0%

870 Mbps

460 Mbps

Excellent

Tokyo

194 ms

8 ms

0.1%

430 Mbps

260 Mbps

Good

Johannesburg

142 ms

9 ms

0%

510 Mbps

300 Mbps

Good

São Paulo

238 ms

13 ms

0.4%

310 Mbps

180 Mbps

Fair

Routes

Show:

traceroute

ASNs

countries

historical route changes

History

Graphs for:

RTT

jitter

packet loss

throughput

DNS

TLS

TTFB

Events

Timeline of detected anomalies.

Targets

Manage:

regions

endpoints

probes

schedules

thresholds

16. Data Model

Target

{
  "id": "tokyo-01",
  "region": "asia-japan",
  "hostname": "nrt1.example.net",
  "ipv4": true,
  "ipv6": true,
  "capabilities": [
    "icmp",
    "tcp",
    "https",
    "bandwidth"
  ]
}

Measurement

{
  "timestamp": "2026-09-10T07:00:00Z",
  "target": "tokyo-01",
  "protocol": "tcp",
  "address_family": "ipv4",
  "latency_ms": 194.2,
  "success": true
}

Route

{
  "target": "tokyo-01",
  "timestamp": "2026-09-10T07:00:00Z",
  "fingerprint": "a829c...",
  "hops": [
    {
      "hop": 1,
      "ip": "192.168.1.1",
      "rtt_ms": 0.8
    }
  ]
}

17. API

Initial REST API:

GET  /api/v1/status
GET  /api/v1/regions
GET  /api/v1/targets
GET  /api/v1/measurements
GET  /api/v1/routes
GET  /api/v1/events

POST /api/v1/tests/run
POST /api/v1/tests/run/{region}

GET  /metrics

Optional live stream:

GET /api/v1/events/stream

using SSE or WebSocket.

18. Prometheus Metrics

Examples:

globalnetbench_latency_ms
globalnetbench_jitter_ms
globalnetbench_packet_loss_ratio
globalnetbench_tcp_connect_ms
globalnetbench_tls_handshake_ms
globalnetbench_http_ttfb_ms
globalnetbench_download_mbps
globalnetbench_upload_mbps
globalnetbench_route_changes_total
globalnetbench_target_up
globalnetbench_region_score
globalnetbench_global_score

Labels:

region
target
protocol
address_family

Avoid high-cardinality labels such as arbitrary IP address per hop in Prometheus.

19. Storage

MVP:

PostgreSQL or SQLite

Recommended initial option:

PostgreSQL

Optional later:

TimescaleDB

Retention example:

retention:
  raw_measurements: 30d
  five_minute_aggregates: 180d
  hourly_aggregates: 3y
  events: forever

20. GeoIP / ASN

Use local GeoIP databases where possible.

Enrich:

public IP

target IP

traceroute hops

Fields:

ASN

ISP / organization

country

city when sufficiently reliable

latitude/longitude for visualization

GeoIP values should be treated as approximate.

21. Configuration Example

server:
  listen: 0.0.0.0:8080

monitoring:
  health_interval: 60s
  quality_interval: 5m
  route_interval: 15m
  bandwidth_interval: 3h

network:
  ipv4: true
  ipv6: true

regions:

  - id: frankfurt
    display_name: Frankfurt
    weight: 1.0

  - id: helsinki
    display_name: Helsinki
    weight: 1.0

  - id: johannesburg
    display_name: Johannesburg
    weight: 1.0

  - id: tokyo
    display_name: Tokyo
    weight: 1.0

  - id: sao-paulo
    display_name: São Paulo
    weight: 1.0

tests:
  icmp:
    enabled: true
    packets: 10

  tcp:
    enabled: true
    ports:
      - 443

  https:
    enabled: true

  traceroute:
    enabled: true

  bandwidth:
    enabled: true
    max_download_mb: 100
    max_upload_mb: 50

database:
  type: postgres

prometheus:
  enabled: true

geoip:
  enabled: true

22. Docker Compose Target

Initial deployment should be possible with:

docker compose up -d

Services:

globalnetbench
postgres
grafana         optional
prometheus      optional

The built-in UI should not require Grafana.

Grafana/Prometheus should be optional integrations rather than mandatory dependencies.

23. MVP

Version 0.1 should implement only:

Dockerized controller

Configurable regions and targets

IPv4/IPv6

ICMP latency

packet loss

jitter

TCP 443 connect latency

HTTPS timing

traceroute

route-change detection

scheduled measurements

PostgreSQL history

REST API

Prometheus endpoint

simple web dashboard

regional health score

global health score

manual "Run Global Test" button

Do not implement remote agents or large bandwidth tests until this core is stable.

24. Version 0.2

Add:

remote probe agent

iperf-compatible bandwidth testing

reverse tests

bidirectional tests

region redundancy

GeoIP/ASN enrichment

world map

alerting

25. Version 0.3

Add:

automated baseline learning

anomaly detection

route correlation

ISP comparison

multiple monitored Internet interfaces/WANs

multi-site comparison

optional hosted/public probe directory

26. Multi-WAN / Multi-Site Future Support

The architecture should eventually allow:

Pegeia Rise fiber
Villa Efrosini Internet
5G backup
Starlink

to be benchmarked independently.

A future dashboard can compare:

                    Fiber       DSL       5G
Frankfurt RTT       48 ms       63 ms     71 ms
Tokyo RTT          194 ms      232 ms    208 ms
Packet loss          0%          1.2%      0.3%
Global score         96          82        88

Tests should therefore identify the originating probe/site/interface.

27. Important Distinction

The application should explicitly separate:

Internet reachability

Can the destination be reached?

Network quality

What are latency, jitter and loss?

Route quality

Which transit path is being used and did it change?

Application quality

How fast are DNS, TCP, TLS and HTTP?

Capacity

How much upload/download bandwidth is available?

These should never be collapsed into only a single "speed" number.

28. Initial Success Criteria

The MVP is successful when a user can deploy it on one Linux machine and, within 10 minutes, see something resembling:

GLOBAL INTERNET HEALTH                94 / 100

Local / Cyprus        8 ms       100
Frankfurt            48 ms        99
Helsinki             73 ms        97
Johannesburg        143 ms        91
Tokyo               194 ms        94
São Paulo           239 ms        86
New York            151 ms        95
Sydney              284 ms        88

IPv4                 HEALTHY
IPv6                 HEALTHY
Packet loss          0.04 %
Route changes        1 today
Internet uptime      99.99 %

The user should immediately be able to tell whether a problem is:

local,

ISP-related,

regional,

international,

or destination-specific.
