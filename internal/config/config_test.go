package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const minimalConfig = `
network:
  ipv4: true
regions:
  - id: eu-central
    targets:
      - hostname: fra.example.net
`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Server.Listen != "0.0.0.0:8080" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Monitoring.HealthInterval.Duration() != time.Minute {
		t.Errorf("health interval = %v, want 1m", cfg.Monitoring.HealthInterval.Duration())
	}
	if cfg.Monitoring.RouteInterval.Duration() != 15*time.Minute {
		t.Errorf("route interval = %v, want 15m", cfg.Monitoring.RouteInterval.Duration())
	}
	if cfg.Database.Type != "sqlite" || cfg.Database.DSN == "" {
		t.Errorf("database = %+v", cfg.Database)
	}
	if cfg.Thresholds.PacketLossWarn != 0.02 {
		t.Errorf("packet loss warn = %v", cfg.Thresholds.PacketLossWarn)
	}

	region := cfg.Regions[0]
	if region.Weight != 1 {
		t.Errorf("weight = %v, want 1", region.Weight)
	}
	if region.DisplayName != "eu-central" {
		t.Errorf("display name = %q, want the region id", region.DisplayName)
	}

	target := region.Targets[0]
	if target.ID != "eu-central-01" {
		t.Errorf("generated target id = %q", target.ID)
	}
	if len(target.Capabilities) == 0 {
		t.Error("expected default capabilities")
	}
}

func TestModelRegionsDerivesTargetDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	regions := cfg.ModelRegions()
	if len(regions) != 1 || len(regions[0].Targets) != 1 {
		t.Fatalf("regions = %+v", regions)
	}

	target := regions[0].Targets[0]
	if target.HTTPSURL != "https://fra.example.net/" {
		t.Errorf("https url = %q", target.HTTPSURL)
	}
	if len(target.Ports) != 1 || target.Ports[0] != 443 {
		t.Errorf("ports = %v, want [443]", target.Ports)
	}
	if !target.Supports(model.CapICMP) {
		t.Error("expected the ICMP capability by default")
	}
}

func TestFamiliesFollowNetworkSettings(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if families := cfg.Families(); len(families) != 1 || families[0] != model.IPv4 {
		t.Errorf("families = %v, want [ipv4]", families)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no address family",
			body: "network:\n  ipv4: false\n  ipv6: false\nregions:\n  - id: a\n    targets:\n      - hostname: a.example\n",
			want: "at least one of ipv4/ipv6",
		},
		{
			name: "no regions",
			body: "network:\n  ipv4: true\n",
			want: "at least one region",
		},
		{
			name: "region without targets",
			body: "network:\n  ipv4: true\nregions:\n  - id: a\n    targets: []\n",
			want: "at least one target",
		},
		{
			name: "target without hostname",
			body: "network:\n  ipv4: true\nregions:\n  - id: a\n    targets:\n      - id: t1\n",
			want: "hostname is required",
		},
		{
			name: "duplicate region",
			body: "network:\n  ipv4: true\nregions:\n  - id: a\n    targets:\n      - hostname: a.example\n  - id: a\n    targets:\n      - hostname: b.example\n",
			want: "duplicate region",
		},
		{
			name: "unknown database",
			body: "network:\n  ipv4: true\ndatabase:\n  type: mysql\nregions:\n  - id: a\n    targets:\n      - hostname: a.example\n",
			want: "must be sqlite or postgres",
		},
		{
			name: "unknown key",
			body: "network:\n  ipv4: true\nnonsense: true\nregions:\n  - id: a\n    targets:\n      - hostname: a.example\n",
			want: "field nonsense not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestDurationParsing(t *testing.T) {
	body := "network:\n  ipv4: true\nmonitoring:\n  health_interval: 90s\n  route_interval: 2h\nregions:\n  - id: a\n    targets:\n      - hostname: a.example\n"
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Monitoring.HealthInterval.Duration() != 90*time.Second {
		t.Errorf("health = %v", cfg.Monitoring.HealthInterval.Duration())
	}
	if cfg.Monitoring.RouteInterval.Duration() != 2*time.Hour {
		t.Errorf("route = %v", cfg.Monitoring.RouteInterval.Duration())
	}
}

func TestInvalidDurationIsRejected(t *testing.T) {
	body := "network:\n  ipv4: true\nmonitoring:\n  health_interval: soon\nregions:\n  - id: a\n    targets:\n      - hostname: a.example\n"
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("expected an error for an unparseable duration")
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}
	if len(cfg.Regions) == 0 {
		t.Error("the example config defines no regions")
	}
	for _, region := range cfg.Regions {
		if len(region.Targets) < 2 {
			t.Errorf("region %s has %d target(s); the example should show at least two per region",
				region.ID, len(region.Targets))
		}
	}
}
