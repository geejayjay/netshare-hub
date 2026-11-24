package sys

import (
	"context"
	"fmt"
	"slices"
)

// Groups (step 1.5) are real Linux groups, so membership shows in
// `id <user>` and files can carry them. Share access through a group is
// expanded to its members when the Samba config and folder ACLs are built:
// the system layer's per-account rules (managed accounts only) hold for
// groups too, and no ACL ever names a group.
//
// NetShare Hub only changes groups it created (the root side keeps the
// list): a group with the same name that exists already is refused, and so
// are the group of managed accounts and the replication account.

// GroupSpec is one managed group and its members (managed account names).
type GroupSpec struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// ValidGroupName reports whether a name can be a managed group.
func ValidGroupName(name string) bool {
	return ValidAccountName(name) && name != ManagedGroup && name != ReplicaAccount
}

// CheckGroups validates a whole list.
func CheckGroups(groups []GroupSpec) error {
	seen := map[string]bool{}
	for _, g := range groups {
		if !ValidGroupName(g.Name) {
			return fmt.Errorf("unsafe group name %q", g.Name)
		}
		if seen[g.Name] {
			return fmt.Errorf("group %q listed twice", g.Name)
		}
		seen[g.Name] = true
		for i, m := range g.Members {
			if !ValidAccountName(m) {
				return fmt.Errorf("group %q: unsafe member %q", g.Name, m)
			}
			if slices.Contains(g.Members[:i], m) {
				return fmt.Errorf("group %q: member %q listed twice", g.Name, m)
			}
		}
	}
	return nil
}

// GroupError says which group could not be made (its name is taken by a
// group NetShare Hub did not create, for example a user's own group).
type GroupError struct {
	Name string
	Err  error
}

func (e *GroupError) Error() string { return "group " + e.Name + ": " + e.Err.Error() }
func (e *GroupError) Unwrap() error { return e.Err }

// Groups manages the Linux groups NetShare Hub owns.
type Groups interface {
	// ApplyGroups makes the managed groups exactly these, with exactly these
	// members: missing groups are created, groups no longer listed are
	// deleted, members are set. Only managed accounts can be members.
	// Errors: ErrInvalid, *GroupError (wrapping ErrNameTaken).
	ApplyGroups(ctx context.Context, groups []GroupSpec) error
}
