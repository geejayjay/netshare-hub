package sysfake

import (
	"context"
	"fmt"
	"slices"

	"netshare-hub/internal/sys"
)

// ApplyGroups checks groups like the real system (managed members only, a
// name taken by a system account's group is refused) and keeps them.
func (f *Fake) ApplyGroups(_ context.Context, groups []sys.GroupSpec) error {
	if err := sys.CheckGroups(groups); err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range groups {
		// Like Linux: every account (system or managed) has a group of its
		// own name, which is not NetShare Hub's.
		if f.system[g.Name] || f.accounts[g.Name] != nil {
			return &sys.GroupError{Name: g.Name, Err: sys.ErrNameTaken}
		}
		for _, m := range g.Members {
			if _, ok := f.accounts[m]; !ok {
				return fmt.Errorf("%w: group %q: %q is not an account NetShare Hub manages", sys.ErrInvalid, g.Name, m)
			}
		}
	}
	if err := f.fail("ApplyGroups"); err != nil {
		return err
	}
	f.groups = map[string][]string{}
	for _, g := range groups {
		f.groups[g.Name] = slices.Clone(g.Members)
	}
	return nil
}

// Groups returns the applied groups and their members.
func (f *Fake) Groups() map[string][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]string{}
	for k, v := range f.groups {
		out[k] = slices.Clone(v)
	}
	return out
}
