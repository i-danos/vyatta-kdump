// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package kdump

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// snapFx wires every seam of snapshotApplyState to a private fixture and counts
// the calls of the later readers. The grub fake logs every call and fails on
// set or unset; the test container runs as root, so errors are injected, not
// provoked with chmod.
type snapFx struct {
	dir       string
	grubLog   string
	queries   int
	lstats    int
	reads     int
	envPath   string
	kexecPath string
}

func newSnapFx(t *testing.T, grubScript string, state string, stateErr error) *snapFx {
	t.Helper()
	dir, err := ioutil.TempDir("", "kdumpsnap")
	if err != nil {
		t.Fatal(err)
	}
	fx := &snapFx{dir: dir, grubLog: filepath.Join(dir, "grub.log"),
		envPath: filepath.Join(dir, "kdump-tools"), kexecPath: filepath.Join(dir, "kexec_crash_loaded")}
	fakeGrubEditEnv(t, `echo "$*" >> `+fx.grubLog+"\n"+
		`case "$*" in *action=set*|*action=unset*) exit 99;; esac`+"\n"+grubScript)

	oldQ, oldKexec, oldEnv, oldL, oldR := queryUnitActiveState, kexecCrashLoadedPath, kdumpEnvFile, lstatKdumpEnv, readKdumpEnv
	queryUnitActiveState = func(string) (string, error) { fx.queries++; return state, stateErr }
	kexecCrashLoadedPath = fx.kexecPath
	kdumpEnvFile = fx.envPath
	lstatKdumpEnv = func(p string) (os.FileInfo, error) { fx.lstats++; return os.Lstat(p) }
	readKdumpEnv = func(p string) ([]byte, error) { fx.reads++; return ioutil.ReadFile(p) }
	t.Cleanup(func() {
		queryUnitActiveState, kexecCrashLoadedPath, kdumpEnvFile, lstatKdumpEnv, readKdumpEnv =
			oldQ, oldKexec, oldEnv, oldL, oldR
		os.RemoveAll(dir)
	})
	return fx
}

func (fx *snapFx) kexec(t *testing.T, content string) {
	t.Helper()
	if err := ioutil.WriteFile(fx.kexecPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func (fx *snapFx) grubCalls(t *testing.T) string {
	b, _ := ioutil.ReadFile(fx.grubLog)
	return string(b)
}

const grubSetScript = `printf 'crashkernel_mem=1G\n'`

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Errorf("err = %v, want one containing %q", err, sub)
	}
}

func TestSnapshotGrubFailureStopsFirst(t *testing.T) {
	fx := newSnapFx(t, `exit 3`, "active", nil)
	snap, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot grubenv")
	if !reflect.DeepEqual(snap, kdumpSnap{}) {
		t.Errorf("not the zero snap: %+v", snap)
	}
	if fx.queries != 0 || fx.lstats != 0 {
		t.Errorf("later readers were called: queries=%d lstats=%d", fx.queries, fx.lstats)
	}
}

func TestSnapshotServiceErrorsStopBeforeKexecAndEnv(t *testing.T) {
	sentinel := errors.New("bus down")
	for name, tc := range map[string]struct {
		state string
		err   error
	}{
		"query error":      {"active", sentinel},
		"failed":           {"failed", nil},
		"activating":       {"activating", nil},
		"deactivating":     {"deactivating", nil},
		"reloading":        {"reloading", nil},
		"empty state text": {"", nil},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newSnapFx(t, grubSetScript, tc.state, tc.err) // no kexec file: must not matter
			snap, err := snapshotApplyState()
			wantErr(t, err, "cannot snapshot service")
			if tc.err != nil && !errors.Is(err, sentinel) {
				t.Errorf("the cause is not wrapped: %v", err)
			}
			if snap.serviceActive || !reflect.DeepEqual(snap, kdumpSnap{}) {
				t.Errorf("not the zero snap: %+v", snap)
			}
			if fx.lstats != 0 {
				t.Errorf("the env file was inspected")
			}
		})
	}
}

func TestSnapshotKexecMissingFileStopsBeforeEnv(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil) // kexec file not written
	_, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot kexec")
	if fx.lstats != 0 {
		t.Errorf("the env file was inspected")
	}
}

func TestSnapshotEnvLstatErrorIsNotAbsent(t *testing.T) {
	sentinel := errors.New("lstat boom")
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	lstatKdumpEnv = func(string) (os.FileInfo, error) { fx.lstats++; return nil, sentinel }
	snap, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot env")
	if !errors.Is(err, sentinel) || snap.envAbsent || fx.reads != 0 {
		t.Errorf("err=%v envAbsent=%v reads=%d", err, snap.envAbsent, fx.reads)
	}
}

func TestSnapshotEnvReadErrorIsNotAbsent(t *testing.T) {
	sentinel := errors.New("read boom")
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	if err := ioutil.WriteFile(fx.envPath, []byte("X=1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	readKdumpEnv = func(string) ([]byte, error) { fx.reads++; return nil, sentinel }
	snap, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot env")
	if !errors.Is(err, sentinel) || snap.envAbsent {
		t.Errorf("err=%v envAbsent=%v", err, snap.envAbsent)
	}
}

// fakeInfo is a regular file whose Sys() is nil.
type fakeInfo struct{}

func (fakeInfo) Name() string       { return "kdump-tools" }
func (fakeInfo) Size() int64        { return 0 }
func (fakeInfo) Mode() os.FileMode  { return 0644 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (fakeInfo) Sys() interface{}   { return nil }

func TestSnapshotEnvWithoutStatTIsAnErrorNotAbsent(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	lstatKdumpEnv = func(string) (os.FileInfo, error) { fx.lstats++; return fakeInfo{}, nil }
	snap, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot env")
	if snap.envAbsent || fx.reads != 0 {
		t.Errorf("envAbsent=%v reads=%d", snap.envAbsent, fx.reads)
	}
}

func TestSnapshotEnvDirectoryAndSymlinkAreUnsupported(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")

	if err := os.Mkdir(fx.envPath, 0755); err != nil {
		t.Fatal(err)
	}
	_, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot env:")
	wantErr(t, err, "unsupported type")
	os.Remove(fx.envPath)

	target := filepath.Join(fx.dir, "target")
	if err := ioutil.WriteFile(target, []byte("SECRET=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fx.envPath); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotApplyState()
	wantErr(t, err, "cannot snapshot env:")
	wantErr(t, err, "unsupported type")
	if snap.envAbsent || len(snap.envBytes) != 0 {
		t.Errorf("the symlink target was followed: %+v", snap)
	}
	if fx.reads != 0 {
		t.Errorf("the file was read (%d times)", fx.reads)
	}
}

func TestSnapshotEnvMissingFileIsAbsent(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	snap, err := snapshotApplyState()
	if err != nil || !snap.envAbsent || fx.reads != 0 || len(snap.envBytes) != 0 {
		t.Errorf("snap=%+v err=%v reads=%d", snap, err, fx.reads)
	}
}

func TestSnapshotRegularEnvFileIsRecordedAndUnchanged(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	content := []byte("KDUMP_RUNTIME=1\nNUM_DUMPS=5\n")
	if err := ioutil.WriteFile(fx.envPath, content, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fx.envPath, 0640); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(fx.envPath)
	st := fi.Sys().(*syscall.Stat_t)

	snap, err := snapshotApplyState()
	if err != nil || snap.envAbsent {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
	if string(snap.envBytes) != string(content) || snap.envMode != fi.Mode() ||
		snap.envUid != st.Uid || snap.envGid != st.Gid {
		t.Errorf("recorded %q %v %d/%d, want %q %v %d/%d", snap.envBytes, snap.envMode, snap.envUid,
			snap.envGid, content, fi.Mode(), st.Uid, st.Gid)
	}
	after, _ := ioutil.ReadFile(fx.envPath)
	fi2, _ := os.Lstat(fx.envPath)
	if string(after) != string(content) || fi2.Mode() != fi.Mode() {
		t.Errorf("the snapshot changed the file")
	}
	if log := fx.grubCalls(t); strings.Contains(log, "set") || strings.Contains(log, "unset") {
		t.Errorf("grub was asked to change something: %q", log)
	}
}

func TestSnapshotEmptyRegularFileIsPresent(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	if err := ioutil.WriteFile(fx.envPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotApplyState()
	if err != nil || snap.envAbsent || len(snap.envBytes) != 0 || fx.reads != 1 {
		t.Errorf("snap=%+v err=%v reads=%d", snap, err, fx.reads)
	}
}

func TestSnapshotFullSuccessFillsEveryField(t *testing.T) {
	fx := newSnapFx(t, grubSetScript, "active", nil)
	fx.kexec(t, "1")
	if err := ioutil.WriteFile(fx.envPath, []byte("A=1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotApplyState()
	if err != nil {
		t.Fatal(err)
	}
	if snap.grubValue != "1G" || !snap.grubSet || !snap.serviceActive || !snap.kexecLoaded ||
		snap.envAbsent || string(snap.envBytes) != "A=1\n" || snap.envMode == 0 {
		t.Errorf("snap = %+v", snap)
	}
	if log := fx.grubCalls(t); strings.TrimSpace(log) != "--running --action=list" {
		t.Errorf("grub calls = %q, want only the list", log)
	}
}

func TestSnapshotUnsetInactiveUnloadedAreSuccesses(t *testing.T) {
	fx := newSnapFx(t, `printf 'other=1\n'`, "inactive", nil)
	fx.kexec(t, "0")
	snap, err := snapshotApplyState()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if snap.grubSet || snap.grubValue != "" || snap.serviceActive || snap.kexecLoaded || !snap.envAbsent {
		t.Errorf("snap = %+v", snap)
	}
}
