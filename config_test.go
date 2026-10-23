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
	"github.com/danos/vyatta-kdump/internal/kdump"
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

// applyFx runs the real applyConfig with every system-touching hook stubbed.
type applyFx struct {
	c                              *Config
	calls                          []string
	snapErr, reserveErr, enableErr error
	disableErr, revertErr          error
	disableCleanup                 bool
	reverted                       *kdump.Snapshot
}

func newApplyFx() *applyFx {
	fx := &applyFx{}
	fx.c = &Config{}
	fx.c.snapshot = func() (kdump.Snapshot, error) {
		fx.calls = append(fx.calls, "snapshot")
		return kdump.Snapshot{}, fx.snapErr
	}
	fx.c.revert = func(s kdump.Snapshot) error {
		fx.calls = append(fx.calls, "revert")
		fx.reverted = &s
		return fx.revertErr
	}
	fx.c.reserve = func(*ConfigData) error { fx.calls = append(fx.calls, "reserve"); return fx.reserveErr }
	fx.c.enable = func(*int, bool) error { fx.calls = append(fx.calls, "enable"); return fx.enableErr }
	fx.c.disable = func(cl bool) error {
		fx.calls = append(fx.calls, "disable")
		fx.disableCleanup = cl
		return fx.disableErr
	}
	return fx
}

func (fx *applyFx) order() string { return strings.Join(fx.calls, ",") }

func TestApplySnapshotFailureAbortsBeforeAnyChange(t *testing.T) {
	fx := newApplyFx()
	fx.snapErr = errors.New("cannot snapshot grubenv: boom")
	err := fx.c.applyConfig(kdumpConfig(true, 5))
	if !errors.Is(err, fx.snapErr) {
		t.Fatalf("err = %v", err)
	}
	if fx.order() != "snapshot" {
		t.Errorf("calls = %s, want only snapshot", fx.order())
	}
}

func TestApplyEnableFailureRevertsAndKeepsOriginalError(t *testing.T) {
	fx := newApplyFx()
	fx.enableErr = errors.New("enable boom")
	fx.revertErr = errors.New("revert env: boom2")
	err := fx.c.applyConfig(kdumpConfig(true, 5))
	if err == nil || !strings.Contains(err.Error(), "enable boom") || !strings.Contains(err.Error(), "boom2") {
		t.Fatalf("err = %v, want both causes", err)
	}
	if fx.order() != "snapshot,reserve,enable,revert" {
		t.Errorf("calls = %s", fx.order())
	}
}

func TestApplyReserveFailureRevertsGrubHalfApply(t *testing.T) {
	fx := newApplyFx()
	fx.reserveErr = errors.New("reserve boom")
	err := fx.c.applyConfig(kdumpConfig(true, 5))
	if err == nil || !strings.Contains(err.Error(), "reserve boom") {
		t.Fatalf("err = %v", err)
	}
	if fx.order() != "snapshot,reserve,enable,revert" {
		t.Errorf("calls = %s", fx.order())
	}
}

func TestApplyDisableFailureReverts(t *testing.T) {
	fx := newApplyFx()
	fx.disableErr = errors.New("stop boom")
	err := fx.c.applyConfig(kdumpConfig(false, 5))
	if err == nil || !strings.Contains(err.Error(), "stop boom") {
		t.Fatalf("err = %v", err)
	}
	if fx.order() != "snapshot,reserve,disable,revert" {
		t.Errorf("calls = %s", fx.order())
	}
}

func TestApplyNilKDumpIsDisabledWithCleanup(t *testing.T) {
	fx := newApplyFx()
	if err := fx.c.applyConfig(&ConfigData{}); err != nil {
		t.Fatal(err)
	}
	if fx.order() != "snapshot,reserve,disable" || !fx.disableCleanup {
		t.Errorf("calls = %s cleanup=%v", fx.order(), fx.disableCleanup)
	}
}

func TestApplySuccessMakesNoRevert(t *testing.T) {
	for _, d := range []*ConfigData{kdumpConfig(true, 5), kdumpConfig(false, 5)} {
		fx := newApplyFx()
		if err := fx.c.applyConfig(d); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fx.order(), "revert") {
			t.Errorf("calls = %s", fx.order())
		}
	}
}

// Set with a failing real applyConfig keeps the cached state, and a nil
// KDump goes through Set to success.
func TestSetThroughApplyConfig(t *testing.T) {
	fx := newApplyFx()
	fx.c.currentConfig.Store(&ConfigData{})
	fx.c.status = func(*ConfigData) string { return "ok" }
	fx.c.apply = fx.c.applyConfig
	if err := fx.c.Set(&ConfigData{}); err != nil {
		t.Fatal(err)
	}
	prev := fx.c.Get()
	fx.enableErr = errors.New("enable boom")
	if err := fx.c.Set(kdumpConfig(true, 5)); err == nil {
		t.Fatal("want error")
	}
	if fx.c.Get() != prev {
		t.Errorf("current config advanced after a failed apply")
	}
}

func TestSetResultNilKDumpDoesNotPanic(t *testing.T) {
	_ = setResult(&ConfigData{})
}

func TestConfigNewWiresApplyHooks(t *testing.T) {
	c := ConfigNew(nil, "")
	if c.snapshot == nil || c.revert == nil || c.reserve == nil || c.enable == nil || c.disable == nil {
		t.Fatalf("ConfigNew left apply hooks unset")
	}
}

// Section 9 item 7: configd reaches kdump through one path (the VCI Set ->
// apply) for forward, compensation and reconcile payloads, so a failed or
// reverted apply must leave Get/cache unchanged whatever the caller was.
func TestSetFailedCompensationAndReconcileKeepCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	fx := newApplyFx()
	fx.c.cacheFile = cache
	fx.c.currentConfig.Store(&ConfigData{})
	fx.c.status = func(*ConfigData) string { return "ok" }
	fx.c.apply = fx.c.applyConfig

	forward := kdumpConfig(true, 5)
	if err := fx.c.Set(forward); err != nil {
		t.Fatal(err)
	}
	before, _ := ioutil.ReadFile(cache)
	for _, payload := range []*ConfigData{{}, kdumpConfig(false, 1)} { // compensation {} / reconcile
		fx.calls = nil
		fx.disableErr = errors.New("stop boom")
		fx.revertErr = nil
		if err := fx.c.Set(payload); err == nil {
			t.Fatal("want error")
		}
		if fx.order() != "snapshot,reserve,disable,revert" {
			t.Errorf("calls = %s", fx.order())
		}
		if fx.c.Get() != forward {
			t.Errorf("current config advanced after a reverted apply")
		}
		if now, _ := ioutil.ReadFile(cache); string(now) != string(before) {
			t.Errorf("cache file changed after a reverted apply")
		}
	}
	// A snapshot (read) error changes nothing at all: no reserve, no
	// Enable/Disable (so no env file removal), cache untouched.
	fx.calls = nil
	fx.snapErr = errors.New("cannot snapshot env: EIO")
	if err := fx.c.Set(&ConfigData{}); err == nil {
		t.Fatal("want error")
	}
	if fx.order() != "snapshot" || fx.c.Get() != forward {
		t.Errorf("calls = %s", fx.order())
	}
}

func TestReadCacheNotAppliedLeavesEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	seed := newApplyFx()
	seed.c.cacheFile = good
	seed.c.currentConfig.Store(&ConfigData{})
	seed.c.status = func(*ConfigData) string { return "" }
	seed.c.apply = seed.c.applyConfig
	if err := seed.c.Set(kdumpConfig(true, 5)); err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{"apply fails": good, "garbled": filepath.Join(dir, "bad"), "missing": filepath.Join(dir, "none")} {
		t.Run(name, func(t *testing.T) {
			if name == "garbled" {
				ioutil.WriteFile(file, []byte(`{"vyatta-system-v1:system": {`), 0600)
			}
			fx := newApplyFx()
			fx.enableErr = errors.New("enable boom")
			fx.c.cacheFile = file
			fx.c.status = func(*ConfigData) string { return "" }
			fx.c.apply = fx.c.applyConfig
			before, _ := ioutil.ReadFile(file)
			fx.c.readCache()
			if got := fx.c.Get(); got.System.KDump != nil {
				t.Errorf("current config advanced: %+v", got.System.KDump)
			}
			if after, _ := ioutil.ReadFile(file); string(after) != string(before) {
				t.Errorf("cache file rewritten")
			}
		})
	}
	ok := newApplyFx()
	ok.c.cacheFile = good
	ok.c.status = func(*ConfigData) string { return "" }
	ok.c.apply = ok.c.applyConfig
	ok.c.readCache()
	if ok.c.Get().System.KDump == nil {
		t.Errorf("a replay that applied must become current")
	}
}
