// Copyright (c) 2021, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package config

import (
	"errors"
	"strconv"
)

type IntOrString interface{}

type KDumpData struct {
	Enable         bool   `rfc7951:"enable"`
	FilesToSave    *int   `rfc7951:"files-to-save,omitempty"`
	DeleteOldFiles bool   `rfc7951:"delete-old-files,emptyleaf"`
	ReservedMemory IntOrString `rfc7951:"reserved-memory,omitempty"`
}

func (cfg *KDumpData) IsEnabled() bool {
	return cfg.Enable && (cfg.FilesToSave == (*int)(nil) || *cfg.FilesToSave != 0)
}

func (cfg *KDumpData) ReservedMemStr() (string, error) {
	switch v := cfg.ReservedMemory.(type) {
	case float64:
		return strconv.FormatUint(uint64(v), 10), nil
	case string:
		return v, nil
	default:
		return "", errors.New("Not a valid type for Reserved Memory")
	}
}
