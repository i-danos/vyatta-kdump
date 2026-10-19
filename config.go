// Copyright (c) 2021, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package vci_kdump

import (
	"errors"
	"fmt"
	"github.com/danos/encoding/rfc7951"
	"github.com/danos/vci"
	cfg "github.com/danos/vyatta-kdump/internal/config"
	"github.com/danos/vyatta-kdump/internal/kdump"
	"github.com/danos/vyatta-kdump/internal/log"
	"io/ioutil"
	"strings"
	"sync"
	"sync/atomic"
)

type Config struct {
	writeMu       sync.Mutex
	currentConfig atomic.Value
	//options
	cacheFile string
	client    *vci.Client

	// Indirections so tests can run Set without touching the system.
	apply  func(*ConfigData) error
	status func(*ConfigData) string
}

var instance_cfg *Config

func ConfigNew(client *vci.Client, cache string) *Config {
	cfg := &Config{}
	cfg.currentConfig.Store(&ConfigData{})

	cfg.client = client
	cfg.cacheFile = cache
	cfg.apply = cfg.applyConfig
	cfg.status = setResult

	cfg.readCache()
	instance_cfg = cfg
	return cfg
}

type ConfigData struct {
	System struct {
		KDump *cfg.KDumpData `rfc7951:"vyatta-system-crash-dump-v1:kernel-crash-dump,omitempty"`
	} `rfc7951:"vyatta-system-v1:system"`
}

func (c *Config) Get() *ConfigData {
	return c.currentConfig.Load().(*ConfigData)
}

// Set applies newConfig. It returns nil when the configuration was applied,
// including when kernel crash dump is configured disabled, and the real apply
// error otherwise: the VCI framework turns a non-nil result into a failed
// commit. The human-readable status text is informational and goes to the log
// only; returning it as an error made every commit fail on a system with
// kernel crash dump disabled.
//
// The cached and current configuration only advance once the configuration
// was applied. After a failed apply they still describe what is in effect, so
// the state reported to operators does not claim a configuration that was
// never applied, and the cache file does not replay it at the next start.
func (c *Config) Set(newConfig *ConfigData) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if newConfig == nil {
		m := c.status(newConfig)
		log.Elog.Println(m)
		return errors.New(m)
	}

	if err := c.apply(newConfig); err != nil {
		log.Elog.Println("Set Error: ", err)
		return err
	}
	c.writeCache(newConfig)
	log.Ilog.Println(c.status(newConfig))
	return nil
}

func (c *Config) Check(proposedConfig *ConfigData) error {
	return nil
}

func (c *Config) applyConfig(cfg *ConfigData) error {
	errs := make([]error, 0)

	if err := reserveMem(cfg); err != nil {
		errs = append(errs, err)
	}

	kd := cfg.System.KDump
	if kd != nil && kd.IsEnabled() {
		if err := kdump.Enable(kd.FilesToSave, kd.DeleteOldFiles); err != nil {
			errs = append(errs, fmt.Errorf("Failed to enable kernel-crash-dump: %s", err))
		}
	} else {
		kdump.Disable(!kd.Enable)
	}

	var err error
	for i, e := range errs {
		if i == 0 {
			err = e
		} else {
			err = fmt.Errorf("%s, %s", err, e)
		}
	}
	return err
}

func reserveMem(cfg *ConfigData) error {
	kd := cfg.System.KDump
	m := "0"
	var err error
	if kd != nil && kd.Enable {
		m, err = kd.ReservedMemStr()
	}
	if err == nil {
		err = kdump.ReserveMem(m)
	}
	if err != nil {
		return fmt.Errorf("Memory reservation error: %s", err)
	}
	return nil
}

func setResult(newcfg *ConfigData) string {
	if newcfg == nil {
		return "Error: No kernel crash dump configuration."
	}

	kd := newcfg.System.KDump

	ready := kdump.GetKDumpState() == kdump.KDumpReady
	reboot := kdump.IsRebootNeeded()
	reserved := kdump.CrashKernelMemory != 0

	// Memory Reservation Check
	mem := ""
	if reboot && reserved && kd.Enable {
		mem = "Reserved Memory changes will take effect on next boot."
	} else if reboot && reserved && !kd.Enable {
		mem = "Reserved memory will be released on next boot."
	} else if reboot && !reserved && kd.Enable {
		mem = "Memory will be reserved on next boot."
	}

	// Enabled or disabled
	state := ""
	if kd.IsEnabled() {
		if ready {
			state = "Kernel crash dump is enabled."
		} else if reboot {
			state = "kernel crash dump will be enabled on next-boot."
		} else {
			state = "ERROR: kernel crash dump is not enabled."
		}
	} else if !ready {
		state = "kernel crash dump is disabled."
	} else {
		state = "Error: Failed to disable kernel crash dump capture."
	}

	return strings.Join([]string{state, mem}, " ")
}

func (c *Config) readCache() {
	cache := &ConfigData{}
	defer func() {
		c.currentConfig.Store(cache)
	}()
	if c.cacheFile == "" {
		return
	}
	buf, err := ioutil.ReadFile(c.cacheFile)
	if err != nil {
		log.Wlog.Println("read-cache:", err)
		return
	}
	err = rfc7951.Unmarshal(buf, cache)
	if err != nil {
		log.Wlog.Println("read-cache:", err)
		return
	}
	err = c.Set(cache)
	if err != nil {
		log.Elog.Println("read-cache:", err)
	}
}

func (c *Config) writeCache(new *ConfigData) {
	c.currentConfig.Store(new)
	if c.cacheFile == "" {
		return
	}
	buf, err := rfc7951.Marshal(new)
	if err != nil {
		log.Elog.Println("write-cache:", err)
		return
	}
	err = ioutil.WriteFile(c.cacheFile, buf, 0600)
	if err != nil {
		log.Elog.Println("write-cache:", err)
	}
}
