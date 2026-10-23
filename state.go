// Copyright (c) 2021, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only

package vci_kdump

import (
	"fmt"
	cf "github.com/danos/vyatta-kdump/internal/config"
	"github.com/danos/vyatta-kdump/internal/kdump"
	st "github.com/danos/vyatta-kdump/internal/state"
)

type State struct {
	conf *Config
}

func StateNew(conf *Config) *State {
	return &State{
		conf: conf,
	}
}

type StateData struct {
	System struct {
		KDump struct {
			KDumpStatus *st.KDumpStatusData `rfc7951:"vyatta-system-crash-dump-v1:status"`
		} `rfc7951:"vyatta-system-crash-dump-v1:kernel-crash-dump"`
	} `rfc7951:"vyatta-system-v1:system"`
}

func (s *State) getConfig() *cf.KDumpData {
	if s.conf != nil {
		conf := s.conf.Get()
		if conf != nil {
			return conf.System.KDump
		}
	}
	return nil
}

func (s *State) serviceState() string {
	kdump_state, err := kdump.GetKDumpState()

	if err == nil && kdump_state == kdump.KDumpReady {
		return "running"
	}

	kdc := s.getConfig()
	if kdc != nil && kdc.IsEnabled() && !kdump.IsRebootNeeded() {
		return "error"
	}
	return "disabled"
}

func (s *State) isLastBootCrashed() bool {
	kdc := s.getConfig()
	if kdc != nil && kdc.IsEnabled() {
		return kdump.LastBootCrashed()
	}
	return false
}

func dateTimeFromName(s string) string {
	if len(s) != 12 {
		return ""
	}
	return fmt.Sprintf("%s-%s-%sT%s:%s:00Z",
		s[:4], s[4:6], s[6:8], s[8:10], s[10:])
}

func getCrashDumps() []st.CrashDumpData {
	crash_dir, files := kdump.GetCrashFiles()
	if len(files) == 0 {
		return nil
	}
	res := make([]st.CrashDumpData, len(files))

	for i, entry := range files {
		sz, _ := kdump.GetCrashSize(entry.Name())
		res[i].Index = uint32(i)
		res[i].Timestamp = dateTimeFromName(entry.Name())
		res[i].Size = uint64(sz)
		res[i].Path = fmt.Sprintf("%s/%s", crash_dir, entry.Name())
	}
	return res
}

func (s *State) getKDumpStatus() *st.KDumpStatusData {
	return &st.KDumpStatusData{
		ServiceState:      s.serviceState(),
		ReservedMemory:    uint64(kdump.CrashKernelMemory),
		NeedReboot:        kdump.IsRebootNeeded(),
		CrashRebootStatus: s.isLastBootCrashed(),
		CrashDumps:        getCrashDumps(),
	}
}

func (s *State) Get() *StateData {
	state := &StateData{}
	state.System.KDump.KDumpStatus = s.getKDumpStatus()
	return state
}
