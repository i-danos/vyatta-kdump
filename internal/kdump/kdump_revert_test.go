// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package kdump

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// revFx stubs every seam of revertApplyState. The grub fake keeps its value in
// a file and logs each call; service state is a variable with start and stop
// counters. The container runs as root, so failures are injected, not
// provoked with chmod.
type revFx struct {
	dir      string
	grubLog  string
	grubVal  string
	envPath  string
	active   bool
	queryErr error
	startErr error
	stopErr  error
	starts   int
	stops    int
	lstatErr error
	readErr  error
}

func newRevFx(t *testing.T) *revFx {
	t.Helper()
	dir, err := ioutil.TempDir("", "kdumprev")
	if err != nil {
		t.Fatal(err)
	}
	fx := &revFx{dir: dir, grubLog: filepath.Join(dir, "grub.log"),
		grubVal: filepath.Join(dir, "grub.val"), envPath: filepath.Join(dir, "kdump-tools")}
	fakeGrubEditEnv(t, `echo "$*" >> `+fx.grubLog+`
case "$*" in
*action=list*) [ -f `+fx.grubVal+` ] && echo "crashkernel_mem=$(cat `+fx.grubVal+`)"; exit 0;;
*action=set*) for a in "$@"; do case "$a" in crashkernel_mem=*) printf %s "${a#crashkernel_mem=}" > `+fx.grubVal+`;; esac; done; exit 0;;
*action=unset*) rm -f `+fx.grubVal+`; exit 0;;
esac`)
	oldQ, oldS, oldP, oldEnv, oldL, oldR := queryUnitActiveState, startKdumpService, stopKdumpService, kdumpEnvFile, lstatKdumpEnv, readKdumpEnv
	queryUnitActiveState = func(string) (string, error) {
		if fx.queryErr != nil {
			return "", fx.queryErr
		}
		if fx.active {
			return "active", nil
		}
		return "inactive", nil
	}
	startKdumpService = func(string) error {
		fx.starts++
		if fx.startErr == nil {
			fx.active = true
		}
		return fx.startErr
	}
	stopKdumpService = func(string) error {
		fx.stops++
		if fx.stopErr == nil {
			fx.active = false
		}
		return fx.stopErr
	}
	kdumpEnvFile = fx.envPath
	lstatKdumpEnv = func(p string) (os.FileInfo, error) {
		if fx.lstatErr != nil {
			return nil, fx.lstatErr
		}
		return os.Lstat(p)
	}
	readKdumpEnv = func(p string) ([]byte, error) {
		if fx.readErr != nil {
			return nil, fx.readErr
		}
		return ioutil.ReadFile(p)
	}
	t.Cleanup(func() {
		queryUnitActiveState, startKdumpService, stopKdumpService, kdumpEnvFile, lstatKdumpEnv, readKdumpEnv =
			oldQ, oldS, oldP, oldEnv, oldL, oldR
		os.RemoveAll(dir)
	})
	return fx
}

func (fx *revFx) setGrub(t *testing.T, v string) {
	t.Helper()
	if err := ioutil.WriteFile(fx.grubVal, []byte(v), 0644); err != nil {
		t.Fatal(err)
	}
}

func (fx *revFx) grubChanges() int {
	b, _ := ioutil.ReadFile(fx.grubLog)
	return strings.Count(string(b), "action=set") + strings.Count(string(b), "action=unset")
}

func (fx *revFx) grubValue() (string, bool) {
	b, err := ioutil.ReadFile(fx.grubVal)
	return string(b), err == nil
}

func (fx *revFx) writeEnv(t *testing.T, data string, mode os.FileMode) {
	t.Helper()
	if err := ioutil.WriteFile(fx.envPath, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fx.envPath, mode); err != nil {
		t.Fatal(err)
	}
}

func envStat(t *testing.T, p string) (os.FileMode, uint32, uint32) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return fi.Mode(), st.Uid, st.Gid
}

// presentSnap's env owner is the test process's own uid/gid: chown to the
// current owner is permitted without privilege, so these cases verify
// content, mode and service state as non-root (e.g. an OBS build user).
// Restoring a different owner needs root and is covered by
// TestRevertRestoresOwner and TestRevertReplayRestoresOwnerModeBytesTwice.
func presentSnap() kdumpSnap {
	return kdumpSnap{grubValue: "1G", grubSet: true, serviceActive: true,
		envBytes: []byte("OLD\n"), envMode: 0640,
		envUid: uint32(os.Geteuid()), envGid: uint32(os.Getegid())}
}

func TestRevertZeroSnapTouchesNothing(t *testing.T) {
	fx := newRevFx(t)
	fx.active = true
	fx.setGrub(t, "9G")
	fx.writeEnv(t, "KEEP", 0644)
	err := revertApplyState(kdumpSnap{})
	wantErr(t, err, "zero snapshot")
	if fx.starts+fx.stops+fx.grubChanges() != 0 {
		t.Errorf("zero snap changed state")
	}
	if b, _ := ioutil.ReadFile(fx.envPath); string(b) != "KEEP" {
		t.Errorf("env changed: %q", b)
	}
}

func TestRevertRestoresEverything(t *testing.T) {
	fx := newRevFx(t)
	fx.active = false
	fx.setGrub(t, "9G")
	fx.writeEnv(t, "NEW", 0600)
	if err := revertApplyState(presentSnap()); err != nil {
		t.Fatal(err)
	}
	if !fx.active || fx.starts != 1 {
		t.Errorf("active=%v starts=%d", fx.active, fx.starts)
	}
	if v, ok := fx.grubValue(); !ok || v != "1G" {
		t.Errorf("grub = %q,%v", v, ok)
	}
	b, _ := ioutil.ReadFile(fx.envPath)
	mode, _, _ := envStat(t, fx.envPath)
	if string(b) != "OLD\n" || mode != 0640 {
		t.Errorf("env = %q mode %v", b, mode)
	}
	// Temp files are cleaned up.
	ents, _ := ioutil.ReadDir(fx.dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "kdump-tools") && e.Name() != "kdump-tools" {
			t.Errorf("leftover temp %s", e.Name())
		}
	}
}

func TestRevertIdempotentSecondCallChangesNothing(t *testing.T) {
	fx := newRevFx(t)
	fx.setGrub(t, "9G")
	fx.writeEnv(t, "NEW", 0600)
	if err := revertApplyState(presentSnap()); err != nil {
		t.Fatal(err)
	}
	starts, stops, changes := fx.starts, fx.stops, fx.grubChanges()
	before, _ := os.Lstat(fx.envPath)
	if err := revertApplyState(presentSnap()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Lstat(fx.envPath)
	if fx.starts != starts || fx.stops != stops || fx.grubChanges() != changes {
		t.Errorf("second call made calls: starts %d stops %d grub %d", fx.starts, fx.stops, fx.grubChanges())
	}
	if !os.SameFile(before, after) {
		t.Errorf("env file was rewritten on a matching state")
	}
}

func TestRevertMatchingStateMakesNoCalls(t *testing.T) {
	fx := newRevFx(t)
	fx.active = true
	fx.setGrub(t, "1G")
	fx.writeEnv(t, "OLD\n", 0640)
	if err := revertApplyState(presentSnap()); err != nil {
		t.Fatal(err)
	}
	if fx.starts+fx.stops+fx.grubChanges() != 0 {
		t.Errorf("calls made for matching state")
	}
}

func TestRevertStopsServiceThatWasInactive(t *testing.T) {
	fx := newRevFx(t)
	fx.active = true
	snap := presentSnap()
	snap.serviceActive = false
	if err := revertApplyState(snap); err != nil {
		t.Fatal(err)
	}
	if fx.stops != 1 || fx.active || fx.starts != 0 {
		t.Errorf("stops=%d starts=%d active=%v", fx.stops, fx.starts, fx.active)
	}
}

func TestRevertAbsentEnvRemovesRegularFile(t *testing.T) {
	fx := newRevFx(t)
	fx.writeEnv(t, "NEW", 0644)
	snap := kdumpSnap{envAbsent: true}
	if err := revertApplyState(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fx.envPath); !os.IsNotExist(err) {
		t.Errorf("env still there: %v", err)
	}
	if err := revertApplyState(snap); err != nil { // ENOENT is success
		t.Errorf("second call: %v", err)
	}
}

func TestRevertAbsentEnvNeverDeletesDirectoryOrSymlink(t *testing.T) {
	for _, kind := range []string{"dir", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			fx := newRevFx(t)
			target := filepath.Join(fx.dir, "target")
			if kind == "dir" {
				os.Mkdir(fx.envPath, 0755)
				ioutil.WriteFile(filepath.Join(fx.envPath, "f"), []byte("x"), 0644)
			} else {
				ioutil.WriteFile(target, []byte("x"), 0644)
				os.Symlink(target, fx.envPath)
			}
			err := revertApplyState(kdumpSnap{envAbsent: true})
			wantErr(t, err, "revert env:")
			wantErr(t, err, "unsupported type")
			if _, e := os.Lstat(fx.envPath); e != nil {
				t.Errorf("non-regular env was removed: %v", e)
			}
			if kind == "symlink" {
				if _, e := os.Lstat(target); e != nil {
					t.Errorf("symlink target removed")
				}
			}
		})
	}
}

func TestRevertLstatErrorIsNotAbsent(t *testing.T) {
	fx := newRevFx(t)
	fx.writeEnv(t, "KEEP", 0644)
	sentinel := errors.New("EIO sentinel")
	fx.lstatErr = sentinel
	err := revertApplyState(kdumpSnap{envAbsent: true})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel wrapped", err)
	}
	wantErr(t, err, "revert env:")
	fx.lstatErr = nil
	if b, _ := ioutil.ReadFile(fx.envPath); string(b) != "KEEP" {
		t.Errorf("file deleted on a read error")
	}
	// present-before with an unreadable env: no write either
	fx.lstatErr = sentinel
	if err := revertApplyState(presentSnap()); !errors.Is(err, sentinel) {
		t.Errorf("present snap: %v", err)
	}
	fx.lstatErr = nil
	if b, _ := ioutil.ReadFile(fx.envPath); string(b) != "KEEP" {
		t.Errorf("file rewritten on a read error")
	}
}

func TestRevertReadErrorIsNotAMismatch(t *testing.T) {
	fx := newRevFx(t)
	fx.writeEnv(t, "KEEP", 0644)
	sentinel := errors.New("read sentinel")
	fx.readErr = sentinel
	err := revertApplyState(presentSnap())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
	fx.readErr = nil
	if b, _ := ioutil.ReadFile(fx.envPath); string(b) != "KEEP" {
		t.Errorf("file rewritten on a read error")
	}
}

func TestRevertEveryStepAttemptedAndAllReported(t *testing.T) {
	fx := newRevFx(t)
	fx.setGrub(t, "9G")
	fx.writeEnv(t, "NEW", 0600)
	sentinel := errors.New("start sentinel")
	fx.startErr = sentinel
	err := revertApplyState(presentSnap())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel wrapped", err)
	}
	wantErr(t, err, "revert service:")
	if strings.Contains(err.Error(), "revert env") || strings.Contains(err.Error(), "revert grubenv") {
		t.Errorf("only the service step failed: %v", err)
	}
	if b, _ := ioutil.ReadFile(fx.envPath); string(b) != "OLD\n" {
		t.Errorf("env step skipped after the service failure")
	}
	if v, _ := fx.grubValue(); v != "1G" {
		t.Errorf("grub step skipped after the service failure")
	}
}

func TestRevertAllThreeFailuresReportedInOrder(t *testing.T) {
	fx := newRevFx(t)
	fx.writeEnv(t, "NEW", 0600)
	svc, env := errors.New("svc sentinel"), errors.New("env sentinel")
	fx.queryErr = svc
	fx.lstatErr = env
	fakeGrubEditEnv(t, `exit 3`)
	err := revertApplyState(presentSnap())
	if !errors.Is(err, svc) || !errors.Is(err, env) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	i, j, k := strings.Index(msg, "revert service:"), strings.Index(msg, "revert env:"), strings.Index(msg, "revert grubenv:")
	if i < 0 || !(i < j && j < k) {
		t.Errorf("order/steps wrong: %q", msg)
	}
	if fx.starts+fx.stops != 0 {
		t.Errorf("service changed without knowing its state")
	}
}

func TestRevertGrubUnsetWhenNotSetBefore(t *testing.T) {
	fx := newRevFx(t)
	fx.setGrub(t, "9G")
	snap := presentSnap()
	snap.grubValue, snap.grubSet = "", false
	if err := revertApplyState(snap); err != nil {
		t.Fatal(err)
	}
	if _, ok := fx.grubValue(); ok {
		t.Errorf("grub value still set")
	}
	n := fx.grubChanges()
	if err := revertApplyState(snap); err != nil || fx.grubChanges() != n {
		t.Errorf("second call: err=%v changes %d->%d", err, n, fx.grubChanges())
	}
}

func TestRevertRestoresOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	fx := newRevFx(t)
	fx.writeEnv(t, "NEW", 0600)
	snap := presentSnap()
	snap.envUid, snap.envGid = 4242, 4343
	if err := revertApplyState(snap); err != nil {
		t.Fatal(err)
	}
	mode, uid, gid := envStat(t, fx.envPath)
	if uid != 4242 || gid != 4343 || mode != 0640 {
		t.Errorf("mode %v uid %d gid %d", mode, uid, gid)
	}
	// owner-only difference is detected and repaired
	if err := os.Chown(fx.envPath, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := revertApplyState(snap); err != nil {
		t.Fatal(err)
	}
	if _, uid, gid = envStat(t, fx.envPath); uid != 4242 || gid != 4343 {
		t.Errorf("owner not repaired: %d %d", uid, gid)
	}
}

func TestRevertRecreatesMissingEnv(t *testing.T) {
	fx := newRevFx(t)
	if err := revertApplyState(presentSnap()); err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadFile(fx.envPath)
	if string(b) != "OLD\n" {
		t.Errorf("env = %q", b)
	}
}

func TestRevertRoundTripWithSnapshot(t *testing.T) {
	fx := newRevFx(t)
	kexec := filepath.Join(fx.dir, "kexec")
	ioutil.WriteFile(kexec, []byte("0\n"), 0644)
	old := kexecCrashLoadedPath
	kexecCrashLoadedPath = kexec
	t.Cleanup(func() { kexecCrashLoadedPath = old })
	fx.setGrub(t, "1G")
	fx.active = true
	fx.writeEnv(t, "ORIG\n", 0640)
	snap, err := snapshotApplyState()
	if err != nil {
		t.Fatal(err)
	}
	fx.setGrub(t, "2G")
	fx.active = false
	fx.writeEnv(t, "CHANGED", 0600)
	if err := revertApplyState(snap); err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadFile(fx.envPath)
	mode, _, _ := envStat(t, fx.envPath)
	v, _ := fx.grubValue()
	if string(b) != "ORIG\n" || mode != 0640 || v != "1G" || !fx.active {
		t.Errorf("env %q mode %v grub %q active %v", b, mode, v, fx.active)
	}
}

// A reconcile or compensation Set replays the revert for the same snapshot:
// bytes, mode and owner come back, a second revert changes nothing, and a
// read error on the env file during the snapshot never leads to a removal.
func TestRevertReplayRestoresOwnerModeBytesTwice(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	fx := newRevFx(t)
	kexec := filepath.Join(fx.dir, "kexec")
	ioutil.WriteFile(kexec, []byte("1\n"), 0644)
	old := kexecCrashLoadedPath
	kexecCrashLoadedPath = kexec
	t.Cleanup(func() { kexecCrashLoadedPath = old })
	fx.setGrub(t, "1G")
	fx.active = true
	fx.writeEnv(t, "ORIG\n", 0640)
	if err := os.Chown(fx.envPath, 4242, 4343); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotApplyState()
	if err != nil {
		t.Fatal(err)
	}
	fx.setGrub(t, "2G")
	fx.active = false
	fx.writeEnv(t, "CHANGED", 0600)
	os.Chown(fx.envPath, 0, 0)
	for i := 0; i < 2; i++ {
		if err := revertApplyState(snap); err != nil {
			t.Fatalf("revert %d: %v", i, err)
		}
	}
	b, _ := ioutil.ReadFile(fx.envPath)
	mode, uid, gid := envStat(t, fx.envPath)
	v, _ := fx.grubValue()
	if string(b) != "ORIG\n" || mode != 0640 || uid != 4242 || gid != 4343 || v != "1G" || !fx.active {
		t.Errorf("env %q %v %d:%d grub %q active %v", b, mode, uid, gid, v, fx.active)
	}
	if fx.starts != 1 || fx.grubChanges() != 1 {
		t.Errorf("second revert made calls: starts %d grub changes %d", fx.starts, fx.grubChanges())
	}
}

func TestSnapshotEnvReadErrorLeavesEnvUntouched(t *testing.T) {
	fx := newRevFx(t)
	kexec := filepath.Join(fx.dir, "kexec")
	ioutil.WriteFile(kexec, []byte("0\n"), 0644)
	old := kexecCrashLoadedPath
	kexecCrashLoadedPath = kexec
	t.Cleanup(func() { kexecCrashLoadedPath = old })
	fx.writeEnv(t, "KEEP", 0644)
	sentinel := errors.New("EIO sentinel")
	fx.lstatErr = sentinel
	if _, err := snapshotApplyState(); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
	fx.lstatErr = nil
	if b, _ := ioutil.ReadFile(fx.envPath); string(b) != "KEEP" {
		t.Errorf("env changed")
	}
	if fx.starts+fx.stops+fx.grubChanges() != 0 {
		t.Errorf("snapshot changed state")
	}
}
