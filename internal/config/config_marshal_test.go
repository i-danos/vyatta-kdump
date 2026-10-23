// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package config

import (
	"strings"
	"testing"

	"github.com/danos/encoding/rfc7951"
)

// The YANG default of "enable" is false. A reconcile read-back compares the
// component's Get with the configuration that was sent, which carries the
// default; the getter therefore must report "enable" even when it is false.
func TestMarshalReportsEnableFalse(t *testing.T) {
	buf, err := rfc7951.Marshal(&KDumpData{ReservedMemory: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(buf)
	if !strings.Contains(s, `"enable":false`) {
		t.Errorf("enable=false is not reported: %s", s)
	}
	if !strings.Contains(s, `"reserved-memory":"auto"`) {
		t.Errorf("reserved-memory missing: %s", s)
	}
	if strings.Contains(s, "delete-old-files") || strings.Contains(s, "files-to-save") {
		t.Errorf("unset leaves must stay absent: %s", s)
	}
}

func TestMarshalRoundTripKeepsValues(t *testing.T) {
	n := 5
	in := &KDumpData{Enable: true, FilesToSave: &n, DeleteOldFiles: true, ReservedMemory: "auto"}
	buf, err := rfc7951.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out KDumpData
	if err := rfc7951.Unmarshal(buf, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Enable || out.FilesToSave == nil || *out.FilesToSave != 5 || !out.DeleteOldFiles {
		t.Errorf("round trip lost values: %+v from %s", out, buf)
	}
}
