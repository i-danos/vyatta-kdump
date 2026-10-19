// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package vci_kdump

import (
	"errors"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"

	kdcfg "github.com/danos/vyatta-kdump/internal/config"
)

// newTestConfig returns a Config whose apply and status hooks do not touch
// the system. applied records every configuration passed to apply.
func newTestConfig(cache string, apply func(*ConfigData) error) (*Config, *[]*ConfigData) {
	var applied []*ConfigData
	c := &Config{cacheFile: cache}
	c.currentConfig.Store(&ConfigData{})
	c.apply = func(d *ConfigData) error {
		applied = append(applied, d)
		return apply(d)
	}
	c.status = func(*ConfigData) string { return "status text, informational" }
	return c, &applied
}

func kdumpConfig(enable bool, files int) *ConfigData {
	d := &ConfigData{}
	d.System.KDump = &kdcfg.KDumpData{Enable: enable, FilesToSave: &files}
	return d
}

// The disabled-by-default case: the first commit applies defaults and the
// component's informational status must not become a commit failure.
func TestSetDisabledSuccessReturnsNil(t *testing.T) {
	c, applied := newTestConfig("", func(*ConfigData) error { return nil })
	d := kdumpConfig(false, 1)
	if err := c.Set(d); err != nil {
		t.Fatalf("Set on successful disabled apply returned %v, want nil", err)
	}
	if len(*applied) != 1 || (*applied)[0] != d {
		t.Fatalf("apply got %v, want exactly the disabled config", *applied)
	}
	if (*applied)[0].System.KDump.IsEnabled() {
		t.Fatalf("config passed to apply is enabled, want disabled")
	}
	if c.Get() != d {
		t.Fatalf("current config not stored after successful Set")
	}
}

func TestSetEnabledSuccessReturnsNil(t *testing.T) {
	c, applied := newTestConfig("", func(*ConfigData) error { return nil })
	d := kdumpConfig(true, 3)
	if err := c.Set(d); err != nil {
		t.Fatalf("Set on successful enabled apply returned %v, want nil", err)
	}
	if len(*applied) != 1 || (*applied)[0] != d {
		t.Fatalf("apply got %v, want exactly the enabled config", *applied)
	}
	kd := (*applied)[0].System.KDump
	if !kd.IsEnabled() || kd.FilesToSave == nil || *kd.FilesToSave != 3 {
		t.Fatalf("config passed to apply = %+v, want enabled with files-to-save 3", kd)
	}
	if c.Get() != d {
		t.Fatalf("current config not stored after successful Set")
	}
}

// A real apply failure is returned, and the cached and current configuration
// do not advance.
func TestSetApplyFailureReturnsErrorAndKeepsState(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "kdump.cfg")
	fail := false
	boom := errors.New("Failed to enable kernel-crash-dump: boom")
	c, _ := newTestConfig(cache, func(*ConfigData) error {
		if fail {
			return boom
		}
		return nil
	})

	good := kdumpConfig(false, 1)
	if err := c.Set(good); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	cached, err := ioutil.ReadFile(cache)
	if err != nil {
		t.Fatalf("cache not written by a successful Set: %v", err)
	}

	fail = true
	bad := kdumpConfig(true, 5)
	err = c.Set(bad)
	if err != boom {
		t.Fatalf("Set returned %v, want the apply error", err)
	}
	if strings.Contains(err.Error(), "status text") {
		t.Fatalf("status text leaked into the error: %v", err)
	}
	if c.Get() != good {
		t.Fatalf("current config advanced after a failed apply")
	}
	after, err := ioutil.ReadFile(cache)
	if err != nil || string(after) != string(cached) {
		t.Fatalf("cache file changed after a failed apply: %q -> %q (%v)",
			cached, after, err)
	}

	// The retry of the same configuration succeeds and then advances.
	fail = false
	if err := c.Set(bad); err != nil {
		t.Fatalf("retry Set: %v", err)
	}
	if c.Get() != bad {
		t.Fatalf("current config not advanced by the successful retry")
	}
	retried, _ := ioutil.ReadFile(cache)
	if string(retried) == string(cached) {
		t.Fatalf("cache file not updated by the successful retry")
	}
}

func TestSetNilConfigIsAnErrorAndKeepsState(t *testing.T) {
	c, applied := newTestConfig("", func(*ConfigData) error { return nil })
	good := kdumpConfig(false, 1)
	if err := c.Set(good); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	c.status = setResult
	if err := c.Set(nil); err == nil {
		t.Fatalf("Set(nil) returned nil, want an error")
	}
	if len(*applied) != 1 {
		t.Fatalf("apply called for a nil config")
	}
	if c.Get() != good {
		t.Fatalf("current config changed by Set(nil)")
	}
}

// ConfigNew must wire the real implementations.
func TestConfigNewWiresHooks(t *testing.T) {
	c := ConfigNew(nil, "")
	if c.apply == nil || c.status == nil {
		t.Fatalf("ConfigNew left apply/status unset")
	}
}
