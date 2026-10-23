// Copyright (c) 2026, AT&T Intellectual Property. All rights reserved.
// SPDX-License-Identifier: GPL-2.0-only
package kdump

import (
	"errors"
	"strings"
	"testing"
)

// stubActiveState replaces the systemd query for one test and records the
// unit it was asked about.
func stubActiveState(t *testing.T, state string, err error) *[]string {
	t.Helper()
	var asked []string
	old := queryUnitActiveState
	queryUnitActiveState = func(unit string) (string, error) {
		asked = append(asked, unit)
		return state, err
	}
	t.Cleanup(func() { queryUnitActiveState = old })
	return &asked
}

func TestServiceActiveQueryErrorIsNotActive(t *testing.T) {
	boom := errors.New("bus down")
	stubActiveState(t, "active", boom) // a state next to an error must not matter
	active, err := serviceActive()
	if active || err != boom {
		t.Errorf("got (%v, %v), want (false, the query error)", active, err)
	}
}

func TestServiceActiveActive(t *testing.T) {
	asked := stubActiveState(t, "active", nil)
	active, err := serviceActive()
	if !active || err != nil {
		t.Errorf("got (%v, %v), want (true, nil)", active, err)
	}
	if len(*asked) != 1 || (*asked)[0] != "vyatta-kdump-load.service" {
		t.Errorf("queried %v, want vyatta-kdump-load.service once", *asked)
	}
}

func TestServiceActiveInactive(t *testing.T) {
	stubActiveState(t, "inactive", nil)
	active, err := serviceActive()
	if active || err != nil {
		t.Errorf("got (%v, %v), want (false, nil)", active, err)
	}
}

func TestServiceActiveUncertainStatesAreErrors(t *testing.T) {
	for _, state := range []string{"failed", "activating", "deactivating", "reloading", ""} {
		stubActiveState(t, state, nil)
		active, err := serviceActive()
		if active || err == nil {
			t.Errorf("state %q: got (%v, %v), want (false, error)", state, active, err)
			continue
		}
		if !strings.Contains(err.Error(), `"`+state+`"`) {
			t.Errorf("state %q: the error does not contain the raw state: %v", state, err)
		}
	}
}
