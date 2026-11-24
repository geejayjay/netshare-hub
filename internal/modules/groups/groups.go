// Package groups manages user groups (step 1.5). A group is a real Linux
// group (membership shows in `id <user>`), created and changed only
// through the system layer; giving a group access to a share gives each of
// its members that access (see the shares module).
package groups

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Group is a user group.
type Group struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name" doc:"Also the Linux group's name."`
	Description string        `json:"description"`
	Members     []GroupMember `json:"members" nullable:"false"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

// GroupMember is one member.
type GroupMember struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
}

const (
	CodeGroupNotFound errs.Code = "GROUP_NOT_FOUND"
	// CodeSyncFailed: the system layer refused or failed; nothing changed.
	CodeSyncFailed errs.Code = "GROUP_SYNC_FAILED"
)

// Users is what groups needs from the users module.
type Users interface {
	GetMany(ctx context.Context, ids []int64) (map[int64]users.User, error)
	GetByUsername(ctx context.Context, username string) (users.User, bool, error)
}

// Shares re-applies share access after membership changes.
type Shares interface {
	Reapply(ctx context.Context) error
}

type Service struct {
	db     *db.DB
	users  Users
	system sys.Groups
	shares Shares
	now    func() time.Time
	mu     sync.Mutex
}

func NewService(d *db.DB, u Users, system sys.Groups) *Service {
	return &Service{db: d, users: u, system: system, now: time.Now}
}

// SetShares connects the shares module (wired after both exist).
func (s *Service) SetShares(sh Shares) { s.shares = sh }

func errNotFound() error { return errs.NotFound(CodeGroupNotFound, "This group does not exist.") }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// all loads every group with its member ids (usernames are filled later).
func (s *Service) all(ctx context.Context, q querier) ([]Group, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name, description, created_at, updated_at FROM user_groups ORDER BY name")
	if err != nil {
		return nil, err
	}
	var out []Group
	index := map[int64]int{}
	for rows.Next() {
		var g Group
		var c, u int64
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &c, &u); err != nil {
			rows.Close()
			return nil, err
		}
		g.CreatedAt, g.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		g.Members = []GroupMember{}
		index[g.ID] = len(out)
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m, err := q.QueryContext(ctx, "SELECT group_id, user_id FROM group_members ORDER BY user_id")
	if err != nil {
		return nil, err
	}
	defer m.Close()
	for m.Next() {
		var gid, uid int64
		if err := m.Scan(&gid, &uid); err != nil {
			return nil, err
		}
		if i, ok := index[gid]; ok {
			out[i].Members = append(out[i].Members, GroupMember{UserID: uid})
		}
	}
	if out == nil {
		out = []Group{}
	}
	return out, m.Err()
}

func (s *Service) fill(ctx context.Context, list []Group) error {
	var ids []int64
	for _, g := range list {
		for _, m := range g.Members {
			ids = append(ids, m.UserID)
		}
	}
	byID, err := s.users.GetMany(ctx, ids)
	if err != nil {
		return err
	}
	for i := range list {
		for j := range list[i].Members {
			list[i].Members[j].Username = byID[list[i].Members[j].UserID].Username
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context) ([]Group, error) {
	list, err := s.all(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	return list, s.fill(ctx, list)
}

func (s *Service) Get(ctx context.Context, id int64) (Group, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Group{}, err
	}
	for _, g := range list {
		if g.ID == id {
			return g, nil
		}
	}
	return Group{}, errNotFound()
}

// Memberships returns every group's members and every group's name (the
// shares module expands group access with it).
func (s *Service) Memberships(ctx context.Context) (members map[int64][]int64, names map[int64]string, err error) {
	list, err := s.all(ctx, s.db.Read)
	if err != nil {
		return nil, nil, err
	}
	members, names = map[int64][]int64{}, map[int64]string{}
	for _, g := range list {
		names[g.ID] = g.Name
		for _, m := range g.Members {
			members[g.ID] = append(members[g.ID], m.UserID)
		}
	}
	return members, names, nil
}

// NameTaken reports whether a group has this name (a new user may not:
// Linux gives every account a group of its own name).
func (s *Service) NameTaken(ctx context.Context, name string) (bool, error) {
	var one int
	err := s.db.Read.QueryRowContext(ctx, "SELECT 1 FROM user_groups WHERE name = ?", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Input creates or changes a group.
type Input struct {
	Name        string
	Description string
	Members     []int64
}

func validDescription(d string) bool {
	if utf8.RuneCountInString(d) > 200 {
		return false
	}
	for _, r := range d {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Service) check(ctx context.Context, v *validate.Errors, id int64, in *Input, checkName bool) error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if checkName {
		v.Check(sys.ValidGroupName(in.Name), "name", "invalid_format",
			"Use 2-32 lower-case letters, digits, - or _, starting with a letter.")
		if !v.Has("name") {
			var one int
			err := s.db.Read.QueryRowContext(ctx, "SELECT 1 FROM user_groups WHERE name = ? AND id != ?", in.Name, id).Scan(&one)
			if err == nil {
				v.Add("name", "taken", "A group with this name already exists.")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, isUser, err := s.users.GetByUsername(ctx, in.Name); err != nil {
				return err
			} else if isUser {
				v.Add("name", "taken_by_user", "A user has this name (Linux gives every user a group of their own name).")
			}
		}
	}
	v.Check(validDescription(in.Description), "description", "invalid_format", "At most 200 characters, on one line.")
	byID, err := s.users.GetMany(ctx, in.Members)
	if err != nil {
		return err
	}
	for i, uid := range in.Members {
		field := fmt.Sprintf("members[%d]", i)
		u, ok := byID[uid]
		switch {
		case slices.Contains(in.Members[:i], uid):
			v.Add(field, "duplicate", "This user is already listed.")
		case !ok:
			v.Add(field, "not_found", "This user does not exist.")
		case !u.SMBAccess:
			v.Add(field, "no_smb_access", "This user has no SMB access, so has no account on the server to put in a group.")
		}
	}
	return nil
}

// change edits the database and makes the Linux groups match, as one step;
// then the shares are applied again (access through groups changed).
func (s *Service) change(ctx context.Context, edit func(tx *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := edit(tx); err != nil {
		return err
	}
	list, err := s.all(ctx, tx)
	if err != nil {
		return err
	}
	if err := s.apply(ctx, list); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		s.restore(ctx)
		return err
	}
	if s.shares != nil {
		if err := s.shares.Reapply(ctx); err != nil && !errs.Is(err, "SAMBA_RELOAD_FAILED") {
			slog.ErrorContext(ctx, "group change saved, but the shares could not be applied again", "err", err)
			return err
		}
	}
	return nil
}

func (s *Service) apply(ctx context.Context, list []Group) error {
	var ids []int64
	for _, g := range list {
		for _, m := range g.Members {
			ids = append(ids, m.UserID)
		}
	}
	byID, err := s.users.GetMany(ctx, ids)
	if err != nil {
		return err
	}
	specs := make([]sys.GroupSpec, 0, len(list))
	for _, g := range list {
		spec := sys.GroupSpec{Name: g.Name, Members: []string{}}
		for _, m := range g.Members {
			if u, ok := byID[m.UserID]; ok && u.SMBAccess {
				spec.Members = append(spec.Members, u.Username)
			}
		}
		specs = append(specs, spec)
	}
	err = s.system.ApplyGroups(ctx, specs)
	var ge *sys.GroupError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ge) && errors.Is(err, sys.ErrNameTaken):
		return validate.New().Add("name", "taken_by_system",
			fmt.Sprintf("The server already has a group called %q that NetShare Hub did not create. Choose another name.", ge.Name)).Err()
	default:
		return errs.Unavailable(CodeSyncFailed, "The Linux groups could not be updated: "+sys.Message(err)+" Nothing was changed.").WithCause(err)
	}
}

// restore makes the Linux groups match the database (after a failed commit).
func (s *Service) restore(ctx context.Context) {
	list, err := s.all(ctx, s.db.Read)
	if err == nil {
		err = s.apply(ctx, list)
	}
	if err != nil {
		slog.ErrorContext(ctx, "Linux groups are out of step with the database", "err", err)
	}
}

func setMembers(ctx context.Context, tx *sql.Tx, id int64, members []int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM group_members WHERE group_id = ?", id); err != nil {
		return err
	}
	for _, uid := range members {
		if _, err := tx.ExecContext(ctx, "INSERT INTO group_members (group_id, user_id) VALUES (?, ?)", id, uid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in Input) (Group, error) {
	v := validate.New()
	if err := s.check(ctx, v, 0, &in, true); err != nil {
		return Group{}, err
	}
	if err := v.Err(); err != nil {
		return Group{}, err
	}
	var id int64
	err := s.change(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO user_groups (name, description, created_at, updated_at) VALUES (?, ?, ?, ?)",
			in.Name, in.Description, s.now().Unix(), s.now().Unix())
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return setMembers(ctx, tx, id, in.Members)
	})
	if err != nil {
		return Group{}, err
	}
	slog.InfoContext(ctx, "group created", "group", in.Name, "members", len(in.Members))
	return s.Get(ctx, id)
}

// Patch changes a group. The name is fixed: it is the Linux group's.
type Patch struct {
	Description *string
	Members     *[]int64
}

func (s *Service) Update(ctx context.Context, id int64, p Patch) (Group, error) {
	g, err := s.Get(ctx, id)
	if err != nil {
		return Group{}, err
	}
	in := Input{Name: g.Name, Description: g.Description}
	for _, m := range g.Members {
		in.Members = append(in.Members, m.UserID)
	}
	if p.Description != nil {
		in.Description = *p.Description
	}
	if p.Members != nil {
		in.Members = *p.Members
	}
	v := validate.New()
	if err := s.check(ctx, v, id, &in, false); err != nil {
		return Group{}, err
	}
	if err := v.Err(); err != nil {
		return Group{}, err
	}
	err = s.change(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE user_groups SET description = ?, updated_at = ? WHERE id = ?",
			in.Description, s.now().Unix(), id); err != nil {
			return err
		}
		if p.Members != nil {
			return setMembers(ctx, tx, id, in.Members)
		}
		return nil
	})
	if err != nil {
		return Group{}, err
	}
	return s.Get(ctx, id)
}

// Delete removes a group: its members lose the access the group gave them.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	return s.change(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM user_groups WHERE id = ?", id)
		return err
	})
}

// RevokeUser takes a user out of every group (the user is losing their
// Linux account, so they cannot stay a member). Part of users.AccessRevoker.
func (s *Service) RevokeUser(ctx context.Context, userID int64) error {
	var n int
	if err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM group_members WHERE user_id = ?", userID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	s.mu.Lock()
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM group_members WHERE user_id = ?", userID); err != nil {
		s.mu.Unlock()
		return err
	}
	list, err := s.all(ctx, tx)
	if err == nil {
		err = s.apply(ctx, list)
	}
	if err == nil {
		err = tx.Commit()
	}
	s.mu.Unlock()
	return err // the shares module applies the shares next (users.AccessRevoker)
}

// Reapply makes the Linux groups match the database (at start).
func (s *Service) Reapply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.all(ctx, s.db.Read)
	if err != nil || len(list) == 0 {
		return err
	}
	return s.apply(ctx, list)
}

// --- routes ---

// GroupRequest creates a group.
type GroupRequest struct {
	Name        string  `json:"name" minLength:"2" maxLength:"32"`
	Description string  `json:"description,omitempty" maxLength:"200"`
	Members     []int64 `json:"members,omitempty" maxItems:"1000" doc:"User ids. Only users with SMB access can be members."`
}

// GroupPatch changes a group (the name is fixed).
type GroupPatch struct {
	Description *string  `json:"description,omitempty" maxLength:"200"`
	Members     *[]int64 `json:"members,omitempty" maxItems:"1000" doc:"Replaces the members."`
}

type (
	idInput struct {
		ID int64 `path:"id" minimum:"1"`
	}
	createInput struct{ Body GroupRequest }
	patchInput  struct {
		ID   int64 `path:"id" minimum:"1"`
		Body GroupPatch
	}
	groupOutput  struct{ Body Group }
	groupsOutput struct{ Body page.List[Group] }
)

func Register(api huma.API, s *Service) {
	const tag = "Groups"
	httpx.Register(api, httpx.Route{ID: "listGroups", Method: http.MethodGet, Path: "/groups", Summary: "List groups", Tag: tag,
		Access: httpx.Require(rbac.GroupsRead)},
		func(ctx context.Context, _ *struct{}) (*groupsOutput, error) {
			list, err := s.List(ctx)
			if err != nil {
				return nil, err
			}
			return &groupsOutput{Body: page.All(list)}, nil
		})
	httpx.Register(api, httpx.Route{ID: "createGroup", Method: http.MethodPost, Path: "/groups", Summary: "Create a group (a Linux group)",
		Tag: tag, Access: httpx.Require(rbac.GroupsWrite), Status: http.StatusCreated},
		func(ctx context.Context, in *createInput) (*groupOutput, error) {
			g, err := s.Create(ctx, Input{Name: in.Body.Name, Description: in.Body.Description, Members: in.Body.Members})
			return &groupOutput{Body: g}, err
		})
	httpx.Register(api, httpx.Route{ID: "getGroup", Method: http.MethodGet, Path: "/groups/{id}", Summary: "Get a group", Tag: tag,
		Access: httpx.Require(rbac.GroupsRead)},
		func(ctx context.Context, in *idInput) (*groupOutput, error) {
			g, err := s.Get(ctx, in.ID)
			return &groupOutput{Body: g}, err
		})
	httpx.Register(api, httpx.Route{ID: "updateGroup", Method: http.MethodPatch, Path: "/groups/{id}",
		Summary: "Change a group's description or members", Tag: tag, Access: httpx.Require(rbac.GroupsWrite)},
		func(ctx context.Context, in *patchInput) (*groupOutput, error) {
			g, err := s.Update(ctx, in.ID, Patch{Description: in.Body.Description, Members: in.Body.Members})
			return &groupOutput{Body: g}, err
		})
	httpx.Register(api, httpx.Route{ID: "deleteGroup", Method: http.MethodDelete, Path: "/groups/{id}",
		Summary: "Delete a group (its members lose the access it gave them)", Tag: tag, Access: httpx.Require(rbac.GroupsWrite),
		Status: http.StatusNoContent},
		func(ctx context.Context, in *idInput) (*struct{}, error) { return nil, s.Delete(ctx, in.ID) })
}

// --- backup and restore ---

// Record is a group in a config backup.
type Record struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Members     []int64 `json:"members"`
}

// Records returns every group for a backup.
func (s *Service) Records(ctx context.Context) ([]Record, error) {
	list, err := s.all(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(list))
	for _, g := range list {
		r := Record{ID: g.ID, Name: g.Name, Description: g.Description, Members: []int64{}}
		for _, m := range g.Members {
			r.Members = append(r.Members, m.UserID)
		}
		out = append(out, r)
	}
	return out, nil
}

// CheckRecords validates the groups of a backup against its users
// (usernames, and which user ids have SMB access). Field names:
// "groups[i].<field>".
func CheckRecords(v *validate.Errors, recs []Record, usernames map[string]bool, smb map[int64]bool) {
	ids, names := map[int64]bool{}, map[string]bool{}
	for i, r := range recs {
		f := fmt.Sprintf("groups[%d].", i)
		v.Check(r.ID > 0 && !ids[r.ID], f+"id", "invalid", "Each group needs its own positive id.")
		ids[r.ID] = true
		v.Check(sys.ValidGroupName(r.Name) && !names[r.Name], f+"name", "invalid_format", "Not a valid group name, or listed twice.")
		names[r.Name] = true
		v.Check(!usernames[r.Name], f+"name", "taken_by_user", "A user in the backup has this name.")
		v.Check(validDescription(r.Description), f+"description", "invalid_format", "Not a valid description.")
		for j, m := range r.Members {
			v.Check(smb[m] && !slices.Contains(r.Members[:j], m), fmt.Sprintf("%smembers[%d]", f, j), "invalid",
				"Not a user with SMB access in the backup, or listed twice.")
		}
	}
}

// ReplaceAll replaces every group, inside the restore's transaction (after
// the users are written).
func ReplaceAll(ctx context.Context, tx *sql.Tx, recs []Record, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_groups"); err != nil {
		return err
	}
	for _, r := range recs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_groups (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			r.ID, r.Name, r.Description, now.Unix(), now.Unix()); err != nil {
			return err
		}
		if err := setMembers(ctx, tx, r.ID, r.Members); err != nil {
			return err
		}
	}
	return nil
}
