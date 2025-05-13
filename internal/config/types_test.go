package config

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestBackoffConfig_ToWaitBackoff(t *testing.T) {
	cfg := BackoffConfig{
		Steps:     3,
		InitialMs: 100,
		Factor:    1.5,
		Jitter:    0.2,
	}

	want := cfg.ToWaitBackoff()

	if want.Steps != 3 {
		t.Errorf("expected Steps=3, got %d", want.Steps)
	}
	if want.Duration != 100*time.Millisecond {
		t.Errorf("expected Duration=100ms, got %s", want.Duration)
	}
	if want.Factor != 1.5 {
		t.Errorf("expected Factor=1.5, got %f", want.Factor)
	}
	if want.Jitter != 0.2 {
		t.Errorf("expected Jitter=0.2, got %f", want.Jitter)
	}
}

func TestDefaultIPAMConfig(t *testing.T) {
	def := DefaultIPAMConfig()

	if def.EnablePoolClaim {
		t.Errorf("EnablePoolClaim should be false in default config")
	}

	// Check nested defaults
	backoff := def.UpdatePoolStatus.Backoff
	expected := BackoffConfig{Steps: 10, InitialMs: 10, Factor: 2.0, Jitter: 0.2}
	if !reflect.DeepEqual(backoff, expected) {
		t.Errorf("default backoff mismatch, want %+v got %+v", expected, backoff)
	}

	if def.UpdatePoolStatus.AsyncTimeoutSec != 3 {
		t.Errorf("default AsyncTimeoutSec should be 3, got %d", def.UpdatePoolStatus.AsyncTimeoutSec)
	}
}

func TestLoadFromFile(t *testing.T) {
	jsonConfig := `{
        "enablePoolClaim": true,
        "updatePoolStatus": {
            "asyncTimeoutSec": 5,
            "backoff": {
                "steps": 5,
                "initialMs": 20,
                "factor": 1.2,
                "jitter": 0.1
            }
        }
    }`

	tmp, err := os.CreateTemp(t.TempDir(), "ipam*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(jsonConfig); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	tmp.Close()

	cfg, err := LoadFromFile(tmp.Name())
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}

	if !cfg.EnablePoolClaim {
		t.Errorf("EnablePoolClaim should be true")
	}

	if cfg.UpdatePoolStatus.AsyncTimeoutSec != 5 {
		t.Errorf("AsyncTimeoutSec mismatch, expected 5 got %d", cfg.UpdatePoolStatus.AsyncTimeoutSec)
	}

	gotBackoff := cfg.UpdatePoolStatus.Backoff
	expectedBackoff := BackoffConfig{Steps: 5, InitialMs: 20, Factor: 1.2, Jitter: 0.1}
	if !reflect.DeepEqual(gotBackoff, expectedBackoff) {
		t.Errorf("backoff mismatch, want %+v got %+v", expectedBackoff, gotBackoff)
	}
}
