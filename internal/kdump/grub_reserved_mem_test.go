// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package kdump

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

// fakeGrubEditEnv points grubEditEnvCmd at a local script for one test.
func fakeGrubEditEnv(t *testing.T, script string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "grubfake")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vyatta-grub-editenv")
	if err := ioutil.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := grubEditEnvCmd
	grubEditEnvCmd = path
	t.Cleanup(func() {
		grubEditEnvCmd = old
		os.RemoveAll(dir)
	})
}

func TestGrubReservedMemUnset(t *testing.T) {
	fakeGrubEditEnv(t, `printf 'other_var=1\n'`)
	v, set, err := GrubReservedMem()
	if v != "" || set || err != nil {
		t.Errorf("got (%q, %v, %v), want (\"\", false, nil)", v, set, err)
	}
}

func TestGrubReservedMemSetValue(t *testing.T) {
	fakeGrubEditEnv(t, `printf 'other_var=1\ncrashkernel_mem=2432-8G:384M,8G-:512M\n'`)
	v, set, err := GrubReservedMem()
	if v != "2432-8G:384M,8G-:512M" || !set || err != nil {
		t.Errorf("got (%q, %v, %v), want the value, true, nil", v, set, err)
	}
}

func TestGrubReservedMemCommandFailureIsAnError(t *testing.T) {
	fakeGrubEditEnv(t, `echo boom >&2; exit 3`)
	v, set, err := GrubReservedMem()
	if err == nil {
		t.Fatalf("a command failure must be an error, got (%q, %v, nil)", v, set)
	}
	if v != "" || set {
		t.Errorf("got (%q, %v) with the error", v, set)
	}
}

func TestIsRebootNeededOnCommandFailureIsNotAMatch(t *testing.T) {
	old := CrashKernelParam
	CrashKernelParam = "" // what a failed read used to be folded into
	defer func() { CrashKernelParam = old }()
	fakeGrubEditEnv(t, `exit 1`)
	if !IsRebootNeeded() {
		t.Errorf("a failed grubenv read must not report that no reboot is needed")
	}

	// success keeps today's comparison
	CrashKernelParam = "2432-8G:384M,8G-:512M"
	fakeGrubEditEnv(t, `printf 'crashkernel_mem=2432-8G:384M,8G-:512M\n'`)
	if IsRebootNeeded() {
		t.Errorf("an equal value means no reboot is needed")
	}
	fakeGrubEditEnv(t, `printf 'crashkernel_mem=1G\n'`)
	if !IsRebootNeeded() {
		t.Errorf("a different value means a reboot is needed")
	}
	fakeGrubEditEnv(t, `printf 'other=1\n'`)
	if !IsRebootNeeded() {
		t.Errorf("an unset value means a reboot is needed")
	}
}
