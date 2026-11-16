/*
 * Copyright 2021 Synology Inc.
 */

package driver

import (
	"errors"
	"testing"

	"github.com/SynologyOpenSource/synology-csi/pkg/dsm/webapi"
)

func rulesFor(clients ...string) []webapi.PrivilegeRule {
	var rules []webapi.PrivilegeRule
	for _, client := range clients {
		rules = append(rules, webapi.PrivilegeRule{Client: client})
	}
	return rules
}

func TestSaveNFSPrivilegeSavesAgainWhenDsmDidNotStoreIt(t *testing.T) {
	clients := []string{"10.0.0.1", "10.0.0.2"}
	saves := 0
	save := func() error { saves++; return nil }
	// The first save is acknowledged but stores nothing.
	load := func() ([]webapi.PrivilegeRule, error) {
		if saves < 2 {
			return nil, nil
		}
		return rulesFor(clients...), nil
	}

	if err := saveNFSPrivilegeVerified(save, load, clients, 5, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saves != 2 {
		t.Errorf("saved %d times, want 2", saves)
	}
}

func TestSaveNFSPrivilegeSavesOnceWhenStored(t *testing.T) {
	clients := []string{"10.0.0.1"}
	saves := 0
	save := func() error { saves++; return nil }
	load := func() ([]webapi.PrivilegeRule, error) { return rulesFor(clients...), nil }

	if err := saveNFSPrivilegeVerified(save, load, clients, 5, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saves != 1 {
		t.Errorf("saved %d times, want 1", saves)
	}
}

func TestSaveNFSPrivilegeGivesUpWithoutErrorWhenNeverStored(t *testing.T) {
	saves := 0
	save := func() error { saves++; return nil }
	load := func() ([]webapi.PrivilegeRule, error) { return rulesFor("10.0.0.1"), nil }

	// The mount decides: a DSM that reports clients in another form must not block it.
	if err := saveNFSPrivilegeVerified(save, load, []string{"10.0.0.1", "10.0.0.2"}, 3, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saves != 3 {
		t.Errorf("saved %d times, want 3", saves)
	}
}

func TestSaveNFSPrivilegeReturnsSaveError(t *testing.T) {
	want := errors.New("share system is busy")
	save := func() error { return want }
	load := func() ([]webapi.PrivilegeRule, error) {
		t.Fatal("load must not run after a failed save")
		return nil, nil
	}

	if err := saveNFSPrivilegeVerified(save, load, []string{"10.0.0.1"}, 2, 0); !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
}
