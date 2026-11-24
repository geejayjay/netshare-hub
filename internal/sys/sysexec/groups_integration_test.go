//go:build integration && linux

package sysexec

// Real Linux groups (step 1.5).

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

func TestRealGroups(t *testing.T) {
	requireRoot(t)
	r := ExecRunner{}
	accounts := NewAccounts(r)
	for _, u := range []string{"nsh-g-ann", "nsh-g-bob"} {
		u := u
		t.Cleanup(func() { _ = accounts.DeleteUser(ctx, u) })
		if err := accounts.CreateUser(ctx, u, "group-password-1"); err != nil {
			t.Fatal(err)
		}
	}
	g := NewGroups(r, t.TempDir()+"/groups.json")
	var restarted []string
	g.changed = func(names []string) { restarted = append(restarted, names...) }
	t.Cleanup(func() { _ = g.ApplyGroups(ctx, nil) })

	if err := g.ApplyGroups(ctx, []sys.GroupSpec{{Name: "nsh-staff", Members: []string{"nsh-g-ann", "nsh-g-bob"}}}); err != nil {
		t.Fatalf("ApplyGroups: %v", err)
	}
	if id := out(t, "id", "-Gn", "nsh-g-ann"); !strings.Contains(id, "nsh-staff") {
		t.Errorf("id: %s", id)
	}
	if len(restarted) != 2 {
		t.Errorf("workers to restart: %v", restarted)
	}

	// Members change; bob leaves.
	restarted = nil
	if err := g.ApplyGroups(ctx, []sys.GroupSpec{{Name: "nsh-staff", Members: []string{"nsh-g-ann"}}}); err != nil {
		t.Fatal(err)
	}
	if id := out(t, "id", "-Gn", "nsh-g-bob"); strings.Contains(id, "nsh-staff") {
		t.Errorf("bob still in the group: %s", id)
	}
	if len(restarted) != 1 || restarted[0] != "nsh-g-bob" {
		t.Errorf("workers to restart: %v", restarted)
	}

	// Refused: a group that exists and is not ours (root, a user's own
	// group), a system account as a member.
	for name, spec := range map[string]sys.GroupSpec{
		"system group":  {Name: "daemon"},
		"user's group":  {Name: "nsh-g-ann"},
		"system member": {Name: "nsh-other", Members: []string{"root"}},
	} {
		err := g.ApplyGroups(ctx, []sys.GroupSpec{spec})
		var ge *sys.GroupError
		if !(errors.As(err, &ge) && errors.Is(err, sys.ErrNameTaken)) && !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, _ := exec.Command("getent", "group", "daemon").CombinedOutput(); !strings.HasPrefix(string(b), "daemon:") {
		t.Fatal("a system group was touched")
	}

	// Gone from the list: the group is deleted (only ours).
	if err := g.ApplyGroups(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("getent", "group", "nsh-staff").Run(); err == nil {
		t.Error("the group is still there")
	}
}
