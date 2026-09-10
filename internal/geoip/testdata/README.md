# Test databases

`GeoLite2-City-Test.mmdb` and `GeoLite2-ASN-Test.mmdb` are the sample databases
from MaxMind's [MaxMind-DB](https://github.com/maxmind/MaxMind-DB) repository,
which is dual-licensed Apache-2.0 / MIT. They contain a handful of synthetic
records and no real subscriber data.

They are vendored so the GeoIP tests exercise real record decoding — the struct
tags in `geoip.go` are the easiest thing in that package to get silently wrong —
without needing a network download at test time.

These are test fixtures only. Running GlobalNetBench with GeoIP enabled needs a
real database; see the GeoIP section of the top-level README.
