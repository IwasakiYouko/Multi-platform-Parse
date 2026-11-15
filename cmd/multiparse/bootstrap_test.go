package main

import (
	"testing"

	"multiparse/internal/store"
)

func TestBootstrapAppliesOnceAndPreservesSavedConfig(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "127.0.0.1")
	t.Setenv("START_PORT", "18080")
	t.Setenv("EXTERNAL_URL", "https://parse.example.com")
	directory := t.TempDir()
	state, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err = bootstrap(state, directory); err != nil {
		t.Fatal(err)
	}
	cfg := state.Config()
	if cfg.Server.Listen != "127.0.0.1" || cfg.Server.Port != 18080 || cfg.Server.ExternalURL != "https://parse.example.com" {
		t.Fatalf("bootstrap did not apply initial server settings: %+v", cfg.Server)
	}
	cfg.Server.Port = 19090
	if err = state.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap(state, directory); err != nil {
		t.Fatal(err)
	}
	if state.Config().Server.Port != 19090 {
		t.Fatal("stale environment overrode saved configuration after bootstrap.done")
	}
}

func TestInvalidBootstrapPortDoesNotCreateMarker(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "127.0.0.1")
	t.Setenv("START_PORT", "99999")
	t.Setenv("EXTERNAL_URL", "")
	directory := t.TempDir()
	state, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err = bootstrap(state, directory); err == nil {
		t.Fatal("invalid port was accepted")
	}
	t.Setenv("START_PORT", "10492")
	if err = bootstrap(state, directory); err != nil {
		t.Fatal(err)
	}
}
