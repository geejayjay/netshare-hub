package shares

import (
	"context"
	"fmt"
	"slices"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/validate"
)

// Group access (step 1.5): a group given access to a share gives each of
// its members that level, unless the member has an entry of their own on
// the share (that one wins, so an admin can give one member less, or
// more). A member of several groups on the share gets the highest level.
// The expansion happens before anything else, so the Samba config, folder
// ACLs, file manager, NFS and the preview all see per-user access.

// ShareGroupAccess is a group's access to a share.
type ShareGroupAccess struct {
	GroupID   int64  `json:"groupId"`
	GroupName string `json:"groupName"`
	Level     Level  `json:"level" enum:"read,upload,write"`
}

// GroupAccessInput gives a group access.
type GroupAccessInput struct {
	GroupID int64
	Level   Level
}

// Groups is what shares needs from the groups module.
type Groups interface {
	// Memberships returns each group's member ids and each group's name.
	Memberships(ctx context.Context) (members map[int64][]int64, names map[int64]string, err error)
}

// SetGroups connects the groups module (wired after both exist).
func (s *Service) SetGroups(g Groups) { s.groups = g }

// memberships returns the groups' members and names (empty without groups).
func (s *Service) memberships(ctx context.Context) (map[int64][]int64, map[int64]string, error) {
	if s.groups == nil {
		return map[int64][]int64{}, map[int64]string{}, nil
	}
	return s.groups.Memberships(ctx)
}

func rank(l Level) int {
	switch l {
	case LevelWrite:
		return 3
	case LevelRead:
		return 2
	case LevelUpload:
		return 1
	}
	return 0
}

// expand returns the per-user access of direct entries plus group access,
// and which group gave each user their access (only for users without an
// entry of their own).
func expand(direct []AccessInput, groups []GroupAccessInput, members map[int64][]int64, names map[int64]string) ([]AccessInput, map[int64]string) {
	out := slices.Clone(direct)
	own := map[int64]bool{}
	for _, a := range direct {
		own[a.UserID] = true
	}
	via := map[int64]string{}
	best := map[int64]int{}
	for _, g := range groups {
		for _, uid := range members[g.GroupID] {
			if own[uid] {
				continue
			}
			if i, ok := best[uid]; ok {
				if rank(g.Level) > rank(out[i].Level) {
					out[i].Level, via[uid] = g.Level, names[g.GroupID]
				}
				continue
			}
			best[uid] = len(out)
			out = append(out, AccessInput{UserID: uid, Level: g.Level})
			via[uid] = names[g.GroupID]
		}
	}
	return out, via
}

func groupInputs(sh Share) []GroupAccessInput {
	out := make([]GroupAccessInput, len(sh.GroupAccess))
	for i, g := range sh.GroupAccess {
		out[i] = GroupAccessInput{GroupID: g.GroupID, Level: g.Level}
	}
	return out
}

// withGroups returns the shares with their access lists expanded (group
// members added), ready for spec.
func (s *Service) withGroups(ctx context.Context, shares []Share) ([]Share, error) {
	members, names, err := s.memberships(ctx)
	if err != nil {
		return nil, err
	}
	return expandShares(shares, members, names), nil
}

func expandShares(shares []Share, members map[int64][]int64, names map[int64]string) []Share {
	out := make([]Share, len(shares))
	for i, sh := range shares {
		access, _ := expand(accessOf(sh), groupInputs(sh), members, names)
		sh.Access = make([]ShareAccess, len(access))
		for j, a := range access {
			sh.Access[j] = ShareAccess{UserID: a.UserID, Level: a.Level}
		}
		out[i] = sh
	}
	return out
}

// checkGroupLevels refuses group levels the share's rules do not allow
// (the same rules as checkUploads and checkTimeMachine for users).
func checkGroupLevels(v *validate.Errors, list []GroupAccessInput, r Rules) {
	for i, g := range list {
		if g.Level != LevelUpload {
			continue
		}
		field := fmt.Sprintf("groupAccess[%d].level", i)
		switch {
		case r.TimeMachine:
			v.Add(field, "not_with_time_machine", "Upload-only does not work for Time Machine. Give \"Read and write\".")
		case r.GuestAccess:
			v.Add(field, "not_with_guest", "Upload-only does not work on a share with guest access: everyone can read what is uploaded.")
		}
	}
}

// expandedUsers returns the per-user access with groups and the users it
// names (for checks that need every user who gets access, like NFS).
func (s *Service) expandedUsers(ctx context.Context, direct []AccessInput, groups []GroupAccessInput,
	members map[int64][]int64, names map[int64]string) ([]AccessInput, map[int64]users.User, error) {
	all, _ := expand(direct, groups, members, names)
	ids := make([]int64, len(all))
	for i, a := range all {
		ids[i] = a.UserID
	}
	byID, err := s.users.GetMany(ctx, ids)
	return all, byID, err
}

// checkGroupAccess validates a share's group access list.
func checkGroupAccess(v *validate.Errors, list []GroupAccessInput, names map[int64]string) {
	for i, g := range list {
		field := fmt.Sprintf("groupAccess[%d]", i)
		switch {
		case slices.ContainsFunc(list[:i], func(o GroupAccessInput) bool { return o.GroupID == g.GroupID }):
			v.Add(field+".groupId", "duplicate", "This group is already listed.")
		case names[g.GroupID] == "":
			v.Add(field+".groupId", "not_found", "This group does not exist.")
		case !g.Level.Valid():
			v.Add(field+".level", "invalid_choice", "Use read, upload or write.")
		}
	}
}
