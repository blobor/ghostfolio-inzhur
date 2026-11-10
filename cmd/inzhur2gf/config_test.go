package main

import (
	"maps"
	"strings"
	"testing"
)

func validEnv() map[string]string {
	return map[string]string{
		envInzhurRefreshToken:    "start-token",
		envGhostfolioURL:         "http://ghostfolio:3333",
		envGhostfolioAccessToken: "access-token",
		envGhostfolioAccountID:   "00000000-0000-0000-0000-000000000000",
		envDataDir:               "/data",
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

func TestLoadConfig(t *testing.T) {
	env := validEnv()
	env[envSymbolMap] = "21=GF_INZHUR-REIT, 36=GF_UA4000238976"
	env[envDryRun] = "true"

	cfg, err := loadConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if got, want := cfg.GhostfolioURL.String(), "http://ghostfolio:3333"; got != want {
		t.Errorf("GhostfolioURL = %q, want %q", got, want)
	}
	if !cfg.DryRun {
		t.Error("DryRun = false, want true")
	}
	want := map[int]string{21: "GF_INZHUR-REIT", 36: "GF_UA4000238976"}
	if !maps.Equal(cfg.SymbolMap, want) {
		t.Errorf("SymbolMap = %v, want %v", cfg.SymbolMap, want)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(getenvFrom(validEnv()))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.DryRun {
		t.Error("DryRun = true, want false")
	}
	if len(cfg.SymbolMap) != 0 {
		t.Errorf("SymbolMap = %v, want empty", cfg.SymbolMap)
	}
}

func TestLoadConfigReportsAllMissingVariables(t *testing.T) {
	_, err := loadConfig(getenvFrom(nil))
	if err == nil {
		t.Fatal("loadConfig() error = nil, want an error")
	}

	for _, name := range []string{
		envInzhurRefreshToken,
		envGhostfolioURL,
		envGhostfolioAccessToken,
		envGhostfolioAccountID,
		envDataDir,
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{"URL without scheme", envGhostfolioURL, "ghostfolio:3333"},
		{"URL without host", envGhostfolioURL, "http://"},
		{"dry run not boolean", envDryRun, "maybe"},
		{"symbol map without symbol", envSymbolMap, "21="},
		{"symbol map without separator", envSymbolMap, "21"},
		{"symbol map with bad ID", envSymbolMap, "reit=GF_INZHUR-REIT"},
		{"symbol map with duplicate ID", envSymbolMap, "21=GF_A,21=GF_B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.env] = tt.value

			_, err := loadConfig(getenvFrom(env))
			if err == nil {
				t.Fatal("loadConfig() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.env) {
				t.Errorf("error does not name %s: %v", tt.env, err)
			}
		})
	}
}
