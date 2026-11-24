package sysexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"netshare-hub/internal/sys"
)

// DefaultGroupsFile lists the groups NetShare Hub created (root only).
const DefaultGroupsFile = "/var/lib/netshare-hub-agent/groups.json"

// Groups is the real sys.Groups: groupadd, gpasswd -M and groupdel.
type Groups struct {
	runner Runner
	file   string
	mu     sync.Mutex
	// changed is called with the accounts whose groups changed (their file
	// workers restart, so they run with the new groups).
	changed func(names []string)
}

func NewGroups(r Runner, file string) *Groups { return &Groups{runner: r, file: file} }

func (g *Groups) owned() (map[string]bool, error) {
	out := map[string]bool{}
	raw, err := os.ReadFile(g.file)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, fmt.Errorf("read %s: %w", g.file, err)
	}
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

func (g *Groups) save(owned map[string]bool) error {
	names := []string{}
	for n := range owned {
		names = append(names, n)
	}
	slices.Sort(names)
	raw, _ := json.Marshal(names) // plain strings: cannot fail
	if err := os.MkdirAll(filepath.Dir(g.file), 0o700); err != nil {
		return err
	}
	return writeAtomicMode(g.file, string(raw), 0o600)
}

// group reads a group: whether it exists, its GID and its members.
func (g *Groups) group(ctx context.Context, name string) (exists bool, gid int, members []string, err error) {
	res, err := g.runner.Run(ctx, Command{Name: "getent", Args: []string{"group", name}})
	if err != nil {
		return false, 0, nil, err
	}
	switch res.ExitCode {
	case 0:
	case 2:
		return false, 0, nil, nil
	default:
		return false, 0, nil, fmt.Errorf("getent group exited with code %d", res.ExitCode)
	}
	// name:x:gid:member,member
	f := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(f) < 4 || f[0] != name {
		return false, 0, nil, fmt.Errorf("unexpected getent output for group %q", name)
	}
	if gid, err = strconv.Atoi(f[2]); err != nil {
		return false, 0, nil, fmt.Errorf("unexpected gid for group %q", name)
	}
	if f[3] != "" {
		members = strings.Split(f[3], ",")
	}
	return true, gid, members, nil
}

func (g *Groups) ApplyGroups(ctx context.Context, groups []sys.GroupSpec) error {
	if err := sys.CheckGroups(groups); err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	// Only managed accounts may be members.
	ids, err := loadIdentities(ctx, g.runner)
	if err != nil {
		return err
	}
	for _, gr := range groups {
		for _, m := range gr.Members {
			uid, ok := ids.uid[m]
			if !ok || !ids.managed[uid] {
				return fmt.Errorf("%w: group %q: %q is not an account NetShare Hub manages", sys.ErrInvalid, gr.Name, m)
			}
		}
	}
	owned, err := g.owned()
	if err != nil {
		return err
	}
	// Refuse a name taken by a group that is not ours before changing
	// anything.
	for _, gr := range groups {
		exists, _, _, err := g.group(ctx, gr.Name)
		if err != nil {
			return err
		}
		if exists && !owned[gr.Name] {
			return &sys.GroupError{Name: gr.Name, Err: sys.ErrNameTaken}
		}
	}

	var changed []string
	want := map[string]bool{}
	for _, gr := range groups {
		want[gr.Name] = true
		exists, gid, before, err := g.group(ctx, gr.Name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := run(ctx, g.runner, "groupadd", "--", gr.Name); err != nil {
				return &sys.GroupError{Name: gr.Name, Err: err}
			}
			owned[gr.Name] = true
			if err := g.save(owned); err != nil {
				return err
			}
			if _, gid, _, err = g.group(ctx, gr.Name); err != nil {
				return err
			}
		}
		if gid < sys.MinManagedUID {
			return &sys.GroupError{Name: gr.Name, Err: fmt.Errorf("%w: it is a system group (gid %d)", sys.ErrNameTaken, gid)}
		}
		after := slices.Clone(gr.Members)
		slices.Sort(after)
		slices.Sort(before)
		if !slices.Equal(before, after) {
			if _, err := run(ctx, g.runner, "gpasswd", "-M", strings.Join(after, ","), gr.Name); err != nil {
				return &sys.GroupError{Name: gr.Name, Err: err}
			}
			changed = append(changed, symmetricDiff(before, after)...)
		}
	}
	for name := range owned {
		if want[name] {
			continue
		}
		_, _, members, err := g.group(ctx, name)
		if err != nil {
			return err
		}
		if _, err := run(ctx, g.runner, "groupdel", "--", name); err != nil && !strings.Contains(err.Error(), "does not exist") {
			return &sys.GroupError{Name: name, Err: err}
		}
		delete(owned, name)
		if err := g.save(owned); err != nil {
			return err
		}
		changed = append(changed, members...)
	}
	if len(changed) > 0 && g.changed != nil {
		slices.Sort(changed)
		g.changed(slices.Compact(changed))
	}
	return nil
}

func symmetricDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			out = append(out, x)
		}
	}
	return out
}
