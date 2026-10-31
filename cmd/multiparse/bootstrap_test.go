package main

import (
	"testing"

	"multiparse/internal/store"
)

func TestCapBootstrapDoesNotOverrideStoredSecurityPolicy(t *testing.T) {
	for _, key := range []string{"LISTEN_ADDR", "START_PORT", "EXTERNAL_URL", "CAP_URL", "CAP_SITE_KEY", "CAP_SECRET"} {
		t.Setenv(key, "")
	}
	t.Setenv("CAP_ENABLED", "false")
	directory := t.TempDir()
	state, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err = bootstrap(state, directory); err != nil {
		t.Fatal(err)
	}
	if !state.Config().Security.CapDisabled {
		t.Fatal("explicit opt-out did not allow password-only deployment")
	}
	config := state.Config()
	config.Security.CapDisabled = false
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap(state, directory); err != nil {
		t.Fatal(err)
	}
	if state.Config().Security.CapDisabled {
		t.Fatal("stale deployment environment overrode the saved CAPTCHA policy")
	}
}

func TestInvalidCapBootstrapCannotDisableCaptcha(t *testing.T) {
	for _, key := range []string{"LISTEN_ADDR", "START_PORT", "EXTERNAL_URL", "CAP_URL", "CAP_SITE_KEY", "CAP_SECRET"} {
		t.Setenv(key, "")
	}
	t.Setenv("CAP_ENABLED", "disabled")
	directory := t.TempDir()
	state, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err = bootstrap(state, directory); err == nil {
		t.Fatal("invalid CAPTCHA policy was accepted")
	}
	if state.Config().Security.CapDisabled {
		t.Fatal("invalid deployment setting weakened CAPTCHA enforcement")
	}
	t.Setenv("CAP_ENABLED", "true")
	if err = bootstrap(state, directory); err != nil {
		t.Fatal(err)
	}
	if state.Config().Security.CapDisabled {
		t.Fatal("corrected bootstrap did not enforce CAPTCHA")
	}
}
