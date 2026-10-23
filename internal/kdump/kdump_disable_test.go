// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package kdump

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disableEnv stubs the service stop and points kdumpEnvFile at a temp path;
// both are restored when the test ends. The returned function reports how
// often the stop was called.
func disableEnv(t *testing.T, stopErr error) (envPath string, stops func() int) {
	t.Helper()
	dir, err := ioutil.TempDir("", "kdumpdisable")
	if err != nil {
		t.Fatal(err)
	}
	envPath = filepath.Join(dir, "kdump-tools")
	n := 0
	oldStop, oldEnv := stopKdumpService, kdumpEnvFile
	stopKdumpService = func(string) error { n++; return stopErr }
	kdumpEnvFile = envPath
	t.Cleanup(func() {
		stopKdumpService, kdumpEnvFile = oldStop, oldEnv
		os.RemoveAll(dir)
	})
	return envPath, func() int { return n }
}

func writeEnv(t *testing.T, path string) {
	t.Helper()
	if err := ioutil.WriteFile(path, []byte("KDUMP_RUNTIME=1\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// makeUnremovable makes os.Remove fail with something other than ENOENT: the
// path is a non-empty directory.
func makeUnremovable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "keep"), 0755); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestDisableStopErrorWithoutCleanupLeavesTheFile(t *testing.T) {
	stopErr := errors.New("stop boom")
	env, stops := disableEnv(t, stopErr)
	writeEnv(t, env)
	if err := Disable(false); err != stopErr {
		t.Errorf("err = %v, want the stop error", err)
	}
	if !exists(env) || stops() != 1 {
		t.Errorf("file exists %v, stops %d", exists(env), stops())
	}
}

func TestDisableStopOkWithoutCleanupLeavesTheFile(t *testing.T) {
	env, stops := disableEnv(t, nil)
	writeEnv(t, env)
	if err := Disable(false); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if !exists(env) || stops() != 1 {
		t.Errorf("file exists %v, stops %d", exists(env), stops())
	}
}

func TestDisableCleanupRemovesTheFile(t *testing.T) {
	env, _ := disableEnv(t, nil)
	writeEnv(t, env)
	if err := Disable(true); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if exists(env) {
		t.Errorf("the env file is still there")
	}
}

func TestDisableCleanupOfAMissingFileIsNotAnError(t *testing.T) {
	disableEnv(t, nil)
	if err := Disable(true); err != nil {
		t.Errorf("err = %v, want nil (already absent)", err)
	}
}

func TestDisableRemoveFailureIsReturned(t *testing.T) {
	env, _ := disableEnv(t, nil)
	makeUnremovable(t, env)
	err := Disable(true)
	if err == nil || os.IsNotExist(err) {
		t.Errorf("err = %v, want the remove error", err)
	}
}

func TestDisableStopErrorStillRemovesTheFile(t *testing.T) {
	stopErr := errors.New("stop boom")
	env, stops := disableEnv(t, stopErr)
	writeEnv(t, env)
	if err := Disable(true); err != stopErr {
		t.Errorf("err = %v, want the stop error", err)
	}
	if exists(env) || stops() != 1 {
		t.Errorf("the file was not removed after a failed stop (exists %v, stops %d)", exists(env), stops())
	}
}

func TestDisableBothFailuresReturnBothCauses(t *testing.T) {
	env, _ := disableEnv(t, errors.New("stop boom"))
	makeUnremovable(t, env)
	err := Disable(true)
	if err == nil {
		t.Fatalf("want an error")
	}
	if !strings.Contains(err.Error(), "stop boom") || !strings.Contains(err.Error(), "directory not empty") {
		t.Errorf("the error lacks one of the causes: %v", err)
	}
}
