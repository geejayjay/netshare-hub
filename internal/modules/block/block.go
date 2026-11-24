// Package block serves disks over iSCSI or NVMe over TCP, each to ONE
// computer (step 5.1): "give one computer a fast dedicated disk". See
// internal/sys/block.go for what the kernel enforces.
//
// Safeguards against the one thing that destroys data here (two computers
// using one disk at once):
//   - every disk names exactly one client (an initiator or host name); the
//     kernel refuses every other client;
//   - creating a disk needs confirmSingleClient, and giving away a whole
//     disk needs the device path typed again (confirmDevice);
//   - a disk in use on this machine (mounted, swap, LVM, RAID) is refused;
//   - an image never lives in a share, where it could be opened as a file
//     while a client writes to it.
package block

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// BlockDisk is a disk served to one client.
type BlockDisk struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol" enum:"iscsi,nvmeof"`
	ImageFolder string `json:"imageFolder,omitempty" doc:"Image-backed: the folder of the image file (<folder>/d<id>.img)."`
	SizeGiB     int    `json:"sizeGiB,omitempty"`
	Device      string `json:"device,omitempty" doc:"A whole disk given to the client, by its /dev/disk/by-id link."`
	Client      string `json:"client" doc:"The one computer that may connect: its iSCSI initiator name (iqn...) or NVMe host name (nqn...)."`
	CHAPUser    string `json:"chapUser,omitempty"`
	CHAP        bool   `json:"chap" doc:"The client must also give the CHAP secret (iSCSI). The secret is never shown again."`
	Listen      string `json:"listen"`
	Port        int    `json:"port"`
	ReadOnly    bool   `json:"readOnly"`
	// Target is what the client connects to.
	Target    string    `json:"target" doc:"The iSCSI target name or NVMe subsystem name to connect to."`
	Connected *bool     `json:"connected,omitempty" doc:"The client is connected now (iSCSI only)."`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	secret    string
}

func (d BlockDisk) sys() sys.BlockDisk {
	return sys.BlockDisk{ID: sysID(d.ID), Protocol: d.Protocol, ImageFolder: d.ImageFolder, SizeGiB: d.SizeGiB, Device: d.Device,
		Client: d.Client, CHAPUser: d.CHAPUser, CHAPSecret: d.secret, Listen: d.Listen, Port: d.Port, ReadOnly: d.ReadOnly}
}

func sysID(id int64) string { return "d" + strconv.FormatInt(id, 10) }

// Status is what this machine can serve.
type BlockStatus struct {
	ISCSI  bool   `json:"iscsi"`
	NVMeOF bool   `json:"nvmeof"`
	Hint   string `json:"hint,omitempty"`
}

const (
	CodeDiskNotFound errs.Code = "BLOCK_DISK_NOT_FOUND"
	CodeUnavailable  errs.Code = "BLOCK_UNAVAILABLE"
	CodeRefused      errs.Code = "BLOCK_DISK_REFUSED"
	alertApply                 = "block.apply_failed"
)

// Alerts is what this module raises.
type Alerts interface {
	Raise(ctx context.Context, in alerts.Raise) (alerts.Alert, error)
	Resolve(ctx context.Context, kind, key string) error
}

// Shares gives the share folders (an image never lives in one).
type Shares interface {
	Paths(ctx context.Context) (map[string]string, error)
}

type Service struct {
	db        *db.DB
	system    sys.Block
	shares    Shares
	alerts    Alerts
	protected []string
	now       func() time.Time
	mu        sync.Mutex
}

func NewService(d *db.DB, system sys.Block, s Shares, a Alerts, protected ...string) *Service {
	return &Service{db: d, system: system, shares: s, alerts: a, protected: protected, now: time.Now}
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const columns = `id, name, protocol, image_folder, size_gib, device, client, chap_user, chap_secret, listen, port, read_only,
	created_at, updated_at`

func (s *Service) all(ctx context.Context, q querier, where string, args ...any) ([]BlockDisk, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM block_disks"+where+" ORDER BY name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlockDisk{}
	for rows.Next() {
		var d BlockDisk
		var created, updated int64
		if err := rows.Scan(&d.ID, &d.Name, &d.Protocol, &d.ImageFolder, &d.SizeGiB, &d.Device, &d.Client, &d.CHAPUser, &d.secret,
			&d.Listen, &d.Port, &d.ReadOnly, &created, &updated); err != nil {
			return nil, err
		}
		d.CHAP = d.secret != ""
		d.Target = d.sys().TargetName()
		d.CreatedAt, d.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) Status(ctx context.Context) (BlockStatus, error) {
	st, err := s.system.BlockStatus(ctx)
	if err != nil {
		return BlockStatus{}, err
	}
	return BlockStatus{ISCSI: st.ISCSI, NVMeOF: st.NVMeOF, Hint: st.Reason}, nil
}

func (s *Service) List(ctx context.Context) ([]BlockDisk, error) {
	list, err := s.all(ctx, s.db.Read, "")
	if err != nil {
		return nil, err
	}
	if st, err := s.system.BlockStatus(ctx); err == nil {
		connected := map[string]*bool{}
		for _, d := range st.Disks {
			connected[d.ID] = d.Connected
		}
		for i := range list {
			list[i].Connected = connected[sysID(list[i].ID)]
		}
	}
	return list, nil
}

func (s *Service) Get(ctx context.Context, id int64) (BlockDisk, error) {
	list, err := s.List(ctx)
	if err != nil {
		return BlockDisk{}, err
	}
	for _, d := range list {
		if d.ID == id {
			return d, nil
		}
	}
	return BlockDisk{}, errs.NotFound(CodeDiskNotFound, "This disk does not exist.")
}

// BlockDiskInput creates a disk.
type BlockDiskInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"64"`
	Protocol    string `json:"protocol" enum:"iscsi,nvmeof" doc:"iSCSI works on Windows, macOS (with an initiator) and Linux, and can require a CHAP secret. NVMe/TCP is faster, but Windows has no built-in client, and it checks only the host name, which a client can copy: use it on a network you trust."`
	ImageFolder string `json:"imageFolder,omitempty" maxLength:"1024" doc:"Make an image file in this folder (not a share)."`
	SizeGiB     int    `json:"sizeGiB,omitempty" minimum:"0" maximum:"65536"`
	Device      string `json:"device,omitempty" maxLength:"300" doc:"Or give a whole disk (see GET /block/devices). Its contents become the client's."`
	Client      string `json:"client" maxLength:"223"`
	CHAPUser    string `json:"chapUser,omitempty" maxLength:"64"`
	CHAPSecret  string `json:"chapSecret,omitempty" maxLength:"16" doc:"12-16 characters (Windows' limit)."`
	Listen      string `json:"listen,omitempty" maxLength:"45" doc:"The address to listen on (default 0.0.0.0: every address)."`
	Port        int    `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Default 3260 (iSCSI) or 4420 (NVMe/TCP)."`
	ReadOnly    bool   `json:"readOnly,omitempty" doc:"iSCSI only."`
	// The safeguards.
	ConfirmSingleClient bool   `json:"confirmSingleClient" doc:"Must be true: only ONE computer may ever use this disk; two at once destroy its contents."`
	ConfirmDevice       string `json:"confirmDevice,omitempty" doc:"When giving a whole disk: its path again."`
}

// BlockDiskPatch changes a disk. The protocol and backing store are fixed.
type BlockDiskPatch struct {
	Name       *string `json:"name,omitempty" maxLength:"64"`
	SizeGiB    *int    `json:"sizeGiB,omitempty" minimum:"1" maximum:"65536" doc:"Grow only: shrinking would cut off the client's data."`
	Client     *string `json:"client,omitempty" maxLength:"223"`
	CHAPUser   *string `json:"chapUser,omitempty" maxLength:"64" doc:"Empty (with an empty secret) turns CHAP off."`
	CHAPSecret *string `json:"chapSecret,omitempty" maxLength:"16"`
	Listen     *string `json:"listen,omitempty" maxLength:"45"`
	Port       *int    `json:"port,omitempty" minimum:"1" maximum:"65535"`
	ReadOnly   *bool   `json:"readOnly,omitempty"`
}

func validName(n string) bool {
	if n == "" || utf8.RuneCountInString(n) > 64 || n != strings.TrimSpace(n) {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// check validates a disk with field errors (the root side checks again).
func (s *Service) check(ctx context.Context, v *validate.Errors, d *BlockDisk, creating bool) error {
	v.Check(validName(d.Name), "name", "invalid_format", "Use 1-64 characters.")
	if !v.Has("name") {
		var one int
		err := s.db.Read.QueryRowContext(ctx, "SELECT 1 FROM block_disks WHERE name = ? AND id != ?", d.Name, d.ID).Scan(&one)
		if err == nil {
			v.Add("name", "taken", "A disk with this name already exists.")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	switch d.Protocol {
	case sys.ProtocolISCSI:
		v.Check(sys.ValidIQN(d.Client), "client", "invalid_format",
			"Enter the client's iSCSI initiator name, like iqn.1991-05.com.microsoft:desktop-jay (Windows: iSCSI Initiator, Configuration tab).")
		if d.CHAPUser != "" || d.secret != "" {
			v.Check(sys.ValidCHAPUser(d.CHAPUser), "chapUser", "invalid_format", "Use 1-64 letters, digits, . _ or -.")
			v.Check(sys.ValidCHAPSecret(d.secret), "chapSecret", "invalid_format", "Use 12-16 characters, without spaces or quotes.")
		}
	case sys.ProtocolNVMeOF:
		v.Check(sys.ValidNQN(d.Client), "client", "invalid_format",
			"Enter the client's NVMe host name (on Linux: cat /etc/nvme/hostnqn).")
		v.Check(d.CHAPUser == "" && d.secret == "", "chapUser", "iscsi_only", "CHAP is for iSCSI disks.")
		v.Check(!d.ReadOnly, "readOnly", "iscsi_only", "Read-only is for iSCSI disks.")
	default:
		v.Add("protocol", "invalid_choice", "Use iscsi or nvmeof.")
	}
	if a, err := netip.ParseAddr(d.Listen); err != nil || a.Zone() != "" {
		v.Add("listen", "invalid_format", "Enter an IP address of this machine, or 0.0.0.0 for every address.")
	} else {
		d.Listen = a.String()
	}
	v.Check(d.Port >= 1 && d.Port <= 65535, "port", "out_of_range", "Use a port from 1 to 65535.")
	if !creating {
		return nil
	}
	switch {
	case d.ImageFolder != "" && d.Device != "":
		v.Add("device", "one_backing", "Choose an image folder or a whole disk, not both.")
	case d.ImageFolder != "":
		if err := s.checkFolder(ctx, v, d.ImageFolder); err != nil {
			return err
		}
		v.Check(d.SizeGiB >= 1 && d.SizeGiB <= sys.MaxDiskGiB, "sizeGiB", "out_of_range", "Use 1 to 65536 GiB.")
	case d.Device != "":
		if !sys.ValidDiskDevice(d.Device) {
			v.Add("device", "invalid_format", "Choose a whole disk from the list (a /dev/disk/by-id path).")
			break
		}
		devices, err := s.system.Devices(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, dev := range devices {
			if dev.Path == d.Device {
				found = true
				if dev.InUse != "" {
					v.Add("device", "in_use", "This disk is in use on this machine: "+dev.InUse+".")
				}
			}
		}
		v.Check(found, "device", "not_found", "There is no such disk.")
	default:
		v.Add("imageFolder", "required", "Choose a folder for an image file, or a whole disk.")
	}
	return nil
}

func (s *Service) checkFolder(ctx context.Context, v *validate.Errors, folder string) error {
	if !sys.ValidSharePath(folder) || !sys.SharablePath(folder) || strings.ContainsAny(folder, " ,=\"'") {
		v.Add("imageFolder", "invalid_format", "Enter a full folder path like /srv/disks, with only letters, digits, . _ - and /.")
		return nil
	}
	for _, p := range s.protected {
		if sys.Overlaps(folder, p) {
			v.Add("imageFolder", "not_allowed", "This folder holds NetShare Hub's own data.")
			return nil
		}
	}
	paths, err := s.shares.Paths(ctx)
	if err != nil {
		return err
	}
	for name, p := range paths {
		if sys.Overlaps(folder, p) {
			v.Add("imageFolder", "overlaps_share", fmt.Sprintf("This folder is, holds or is inside share %q: a disk image must not be reachable as a file.", name))
			return nil
		}
	}
	return nil
}

// change edits the database and makes the kernel serve the result, as one
// step: if the kernel refuses, the edit is rolled back and the committed
// disks are served again.
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
	disks, err := s.all(ctx, tx, "")
	if err != nil {
		return err
	}
	if err := s.apply(ctx, disks); err != nil {
		_ = tx.Rollback()
		s.restore(ctx)
		return err
	}
	return tx.Commit()
}

func (s *Service) apply(ctx context.Context, disks []BlockDisk) error {
	out := make([]sys.BlockDisk, 0, len(disks))
	for _, d := range disks {
		out = append(out, d.sys())
	}
	err := s.system.ApplyDisks(ctx, out)
	if err == nil {
		return nil
	}
	msg := sys.Message(err)
	var de *sys.DiskError
	if errors.As(err, &de) {
		msg = sys.Message(de.Err)
	}
	switch {
	case errors.Is(err, sys.ErrNotFound):
		return errs.Conflict(CodeUnavailable, "Block storage is not available here: "+msg).WithCause(err)
	case errors.Is(err, sys.ErrInvalid):
		return errs.Invalid(CodeRefused, "The disk was refused: "+msg+" Nothing was changed.").WithCause(err)
	}
	return errs.Internal(fmt.Errorf("apply disks: %w", err))
}

// restore serves what the database holds (after a refused change).
func (s *Service) restore(ctx context.Context) {
	disks, err := s.all(ctx, s.db.Read, "")
	if err == nil {
		err = s.apply(ctx, disks)
	}
	if err != nil {
		slog.ErrorContext(ctx, "block disks are out of step with the database", "err", err)
	}
}

// Reapply serves the stored disks (at start: the kernel forgets its
// targets at every reboot).
func (s *Service) Reapply(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	disks, err := s.all(ctx, s.db.Read, "")
	if err != nil || len(disks) == 0 {
		return
	}
	err = s.apply(ctx, disks)
	if s.alerts == nil {
		return
	}
	if err == nil {
		_ = s.alerts.Resolve(ctx, alertApply, "")
		return
	}
	slog.ErrorContext(ctx, "could not serve the block disks", "err", err)
	_, _ = s.alerts.Raise(ctx, alerts.Raise{Kind: alertApply, Severity: alerts.Critical, Source: alerts.SourceStorage,
		Title:   "Block disks are not being served",
		Message: "NetShare Hub could not set up its iSCSI / NVMe disks after starting, so their computers cannot reach them: " + errs.From(err).Message})
}

func (s *Service) Create(ctx context.Context, in BlockDiskInput) (BlockDisk, error) {
	d := BlockDisk{Name: strings.TrimSpace(in.Name), Protocol: in.Protocol, ImageFolder: strings.TrimSpace(in.ImageFolder),
		SizeGiB: in.SizeGiB, Device: strings.TrimSpace(in.Device), Client: strings.TrimSpace(in.Client), CHAPUser: in.CHAPUser,
		secret: in.CHAPSecret, Listen: strings.TrimSpace(in.Listen), Port: in.Port, ReadOnly: in.ReadOnly}
	if d.Listen == "" {
		d.Listen = "0.0.0.0"
	}
	if d.Port == 0 {
		d.Port = map[bool]int{true: 4420, false: 3260}[d.Protocol == sys.ProtocolNVMeOF]
	}
	v := validate.New()
	v.Check(in.ConfirmSingleClient, "confirmSingleClient", "required",
		"Confirm that only one computer will ever use this disk: two at once destroy its contents.")
	if d.Device != "" {
		v.Check(in.ConfirmDevice == d.Device, "confirmDevice", "mismatch",
			"Type the disk's path again to confirm: everything on it becomes the client's.")
	}
	if err := s.check(ctx, v, &d, true); err != nil {
		return BlockDisk{}, err
	}
	if err := v.Err(); err != nil {
		return BlockDisk{}, err
	}
	var id int64
	err := s.change(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO block_disks (name, protocol, image_folder, size_gib, device, client, chap_user,
			chap_secret, listen, port, read_only, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Name, d.Protocol, d.ImageFolder, d.SizeGiB, d.Device, d.Client, d.CHAPUser, d.secret, d.Listen, d.Port, d.ReadOnly,
			s.now().Unix(), s.now().Unix())
		if err == nil {
			id, err = res.LastInsertId()
		}
		return err
	})
	if err != nil {
		return BlockDisk{}, err
	}
	slog.InfoContext(ctx, "block disk created", "disk", d.Name, "protocol", d.Protocol, "client", d.Client, "device", d.Device,
		"image_folder", d.ImageFolder)
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, in BlockDiskPatch) (BlockDisk, error) {
	list, err := s.all(ctx, s.db.Read, " WHERE id = ?", id)
	if err != nil {
		return BlockDisk{}, err
	}
	if len(list) == 0 {
		return BlockDisk{}, errs.NotFound(CodeDiskNotFound, "This disk does not exist.")
	}
	d := list[0]
	v := validate.New()
	if in.Name != nil {
		d.Name = strings.TrimSpace(*in.Name)
	}
	if in.SizeGiB != nil {
		switch {
		case d.ImageFolder == "":
			v.Add("sizeGiB", "not_image", "Only an image-backed disk has a size to change.")
		case *in.SizeGiB < d.SizeGiB:
			v.Add("sizeGiB", "no_shrink", "A disk can only grow: shrinking would cut off the client's data.")
		default:
			d.SizeGiB = *in.SizeGiB
		}
	}
	if in.Client != nil {
		d.Client = strings.TrimSpace(*in.Client)
	}
	if in.CHAPUser != nil {
		d.CHAPUser = *in.CHAPUser
	}
	if in.CHAPSecret != nil {
		d.secret = *in.CHAPSecret
	}
	if in.Listen != nil {
		d.Listen = strings.TrimSpace(*in.Listen)
	}
	if in.Port != nil {
		d.Port = *in.Port
	}
	if in.ReadOnly != nil {
		d.ReadOnly = *in.ReadOnly
	}
	if err := s.check(ctx, v, &d, false); err != nil {
		return BlockDisk{}, err
	}
	if err := v.Err(); err != nil {
		return BlockDisk{}, err
	}
	err = s.change(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE block_disks SET name = ?, size_gib = ?, client = ?, chap_user = ?, chap_secret = ?,
			listen = ?, port = ?, read_only = ?, updated_at = ? WHERE id = ?`,
			d.Name, d.SizeGiB, d.Client, d.CHAPUser, d.secret, d.Listen, d.Port, d.ReadOnly, s.now().Unix(), id)
		return err
	})
	if err != nil {
		return BlockDisk{}, err
	}
	return s.Get(ctx, id)
}

// Delete stops serving a disk (its client is disconnected). An image file
// is kept: it holds the client's data.
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.change(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM block_disks WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errs.NotFound(CodeDiskNotFound, "This disk does not exist.")
		}
		return nil
	})
}

// --- routes ---

type (
	idInput struct {
		ID int64 `path:"id" minimum:"1"`
	}
	createInput struct {
		Body BlockDiskInput
	}
	patchInput struct {
		ID   int64 `path:"id" minimum:"1"`
		Body BlockDiskPatch
	}
	diskOutput struct {
		Body BlockDisk
	}
	disksOutput struct {
		Body page.List[BlockDisk]
	}
	devicesOutput struct {
		Body page.List[sys.DiskDevice]
	}
	statusOutput struct {
		Body BlockStatus
	}
)

// Register adds the routes (all need block:manage, Admin only).
func Register(api huma.API, s *Service) {
	const tag = "Block storage"
	r := func(id, method, path, summary string, status int) httpx.Route {
		return httpx.Route{ID: id, Method: method, Path: path, Summary: summary, Tag: tag, Access: httpx.Require(rbac.BlockManage), Status: status}
	}
	httpx.Register(api, r("getBlockStatus", http.MethodGet, "/block/status", "Whether this machine can serve iSCSI / NVMe disks", 0),
		func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
			st, err := s.Status(ctx)
			return &statusOutput{Body: st}, err
		})
	httpx.Register(api, r("listBlockDevices", http.MethodGet, "/block/devices", "Whole disks on this machine, and whether each is in use", 0),
		func(ctx context.Context, _ *struct{}) (*devicesOutput, error) {
			list, err := s.system.Devices(ctx)
			if err != nil {
				return nil, err
			}
			return &devicesOutput{Body: page.All(list)}, nil
		})
	httpx.Register(api, r("listBlockDisks", http.MethodGet, "/block/disks", "List disks", 0),
		func(ctx context.Context, _ *struct{}) (*disksOutput, error) {
			list, err := s.List(ctx)
			if err != nil {
				return nil, err
			}
			return &disksOutput{Body: page.All(list)}, nil
		})
	httpx.Register(api, r("createBlockDisk", http.MethodPost, "/block/disks", "Create a disk for one computer", http.StatusCreated),
		func(ctx context.Context, in *createInput) (*diskOutput, error) {
			d, err := s.Create(ctx, in.Body)
			return &diskOutput{Body: d}, err
		})
	httpx.Register(api, r("getBlockDisk", http.MethodGet, "/block/disks/{id}", "Get a disk", 0),
		func(ctx context.Context, in *idInput) (*diskOutput, error) {
			d, err := s.Get(ctx, in.ID)
			return &diskOutput{Body: d}, err
		})
	httpx.Register(api, r("updateBlockDisk", http.MethodPatch, "/block/disks/{id}", "Change a disk (its client is reconnected)", 0),
		func(ctx context.Context, in *patchInput) (*diskOutput, error) {
			d, err := s.Update(ctx, in.ID, in.Body)
			return &diskOutput{Body: d}, err
		})
	httpx.Register(api, r("deleteBlockDisk", http.MethodDelete, "/block/disks/{id}",
		"Stop serving a disk (an image file is kept)", http.StatusNoContent),
		func(ctx context.Context, in *idInput) (*struct{}, error) { return nil, s.Delete(ctx, in.ID) })
}
