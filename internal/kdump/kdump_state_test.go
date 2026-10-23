// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package kdump

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

// stateFile points kexecCrashLoadedPath at a temp file with the given content
// (nil content = the file does not exist) for one test.
func stateFile(t *testing.T, content *string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "kexecstate")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "kexec_crash_loaded")
	if content != nil {
		if err := ioutil.WriteFile(path, []byte(*content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := kexecCrashLoadedPath
	kexecCrashLoadedPath = path
	t.Cleanup(func() {
		kexecCrashLoadedPath = old
		os.RemoveAll(dir)
	})
}

func str(s string) *string { return &s }

func TestGetKDumpStateMissingFileIsAnError(t *testing.T) {
	stateFile(t, nil)
	st, err := GetKDumpState()
	if err == nil {
		t.Fatalf("got (%d, nil), want an error", st)
	}
	if st == KDumpReady {
		t.Errorf("an error must not come with KDumpReady")
	}
}

func TestGetKDumpStateOneIsReady(t *testing.T) {
	for _, c := range []string{"1", "1\n", " 1 \n"} {
		stateFile(t, str(c))
		if st, err := GetKDumpState(); st != KDumpReady || err != nil {
			t.Errorf("content %q: got (%d, %v), want (KDumpReady, nil)", c, st, err)
		}
	}
}

func TestGetKDumpStateOtherIntegersAreNotReady(t *testing.T) {
	for _, c := range []string{"0", "0\n", "2", "-1"} {
		stateFile(t, str(c))
		if st, err := GetKDumpState(); st != KDumpNotReady || err != nil {
			t.Errorf("content %q: got (%d, %v), want (KDumpNotReady, nil)", c, st, err)
		}
	}
}

func TestGetKDumpStateNonIntegerIsAnError(t *testing.T) {
	for _, c := range []string{"loaded", "", "1x", "0x1"} {
		stateFile(t, str(c))
		st, err := GetKDumpState()
		if err == nil {
			t.Errorf("content %q: got (%d, nil), want an error", c, st)
		}
		if st == KDumpReady {
			t.Errorf("content %q: an error must not come with KDumpReady", c)
		}
	}
}
