package cli

import (
	"slices"
	"testing"
)

func TestChangeOwners(t *testing.T) {
	got, err := changeOwners([]string{"alice"}, []string{"bob", "alice"}, nil)
	if err != nil || !slices.Equal(got, []string{"alice", "bob"}) {
		t.Errorf("add = %v, %v", got, err)
	}
	got, err = changeOwners([]string{"alice", "bob"}, []string{"carol"}, []string{"alice"})
	if err != nil || !slices.Equal(got, []string{"bob", "carol"}) {
		t.Errorf("add and remove = %v, %v", got, err)
	}
	if _, err := changeOwners([]string{"alice"}, nil, []string{"alice"}); err == nil {
		t.Error("removed the last owner")
	}
	if _, err := changeOwners([]string{"alice"}, nil, []string{"mallory"}); err == nil {
		t.Error("removed someone who isn't an owner")
	}
	// An app without owners (an admin's) gets its first.
	got, err = changeOwners(nil, []string{"alice"}, nil)
	if err != nil || !slices.Equal(got, []string{"alice"}) {
		t.Errorf("first owner = %v, %v", got, err)
	}
}
