package guard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/ratelimit"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
)

// SMB brute-force protection (2.4).
//
// Failed SMB logins are counted per address (an IPv4 address, or an IPv6
// /64: one attacker controls a whole /64) with the same limiter the web
// login uses. Too many within the window and the address is banned: Samba
// refuses it when it connects, before it can try a password ("hosts deny"
// in the managed config), and its open connections are dropped. Each new
// ban of the same address or device lasts 4 times longer, up to a maximum.
//
// DHCP can give a banned computer a new address. When the computer is on
// the local network, the ban records its hardware (MAC) address, and a new
// address seen for the same device is banned at once. Enforcement stays by
// address: a MAC cannot be filtered by Samba, and changing one is trivial.
//
// The allow list is never banned (an admin's own computer), and neither is
// the server itself (127.0.0.1, ::1).

// BanSettings are the brute-force protection settings.
type BanSettings struct {
	Enabled       bool      `json:"enabled" doc:"Count failed SMB logins and ban addresses that fail too often. Also turns on login logging in Samba."`
	Failures      int       `json:"failures" minimum:"3" maximum:"1000" doc:"Failed logins from one address ..."`
	WindowMinutes int       `json:"windowMinutes" minimum:"1" maximum:"1440" doc:"... within this many minutes, to be banned."`
	BanMinutes    int       `json:"banMinutes" minimum:"1" maximum:"10080" doc:"How long the first ban lasts. Each repeat within a week lasts 4 times longer."`
	MaxBanHours   int       `json:"maxBanHours" minimum:"1" maximum:"8760" doc:"The longest a ban lasts."`
	FollowDevice  bool      `json:"followDevice" doc:"When a banned computer on the local network gets a new address (DHCP), ban the new address too (recognised by its hardware/MAC address)."`
	AllowList     []string  `json:"allowList" nullable:"false" maxItems:"64" doc:"Addresses and ranges never banned (your own computer). IPs or CIDR ranges."`
	UpdatedAt     time.Time `json:"updatedAt,omitzero"`
}

// Ban is one banned address.
type Ban struct {
	ID       int64      `json:"id"`
	Address  string     `json:"address" doc:"An IPv4 address, or an IPv6 /64 range."`
	MAC      string     `json:"mac,omitempty" doc:"The device's hardware address, when it is on the local network."`
	Reason   string     `json:"reason" enum:"failures,same_device" doc:"\"same_device\": a banned device came back with a new address."`
	Failures int        `json:"failures"`
	Users    []string   `json:"users" nullable:"false" doc:"The account names tried."`
	Offence  int        `json:"offence" doc:"1 for a first ban; higher for repeats within a week (longer bans)."`
	BannedAt time.Time  `json:"bannedAt"`
	Until    time.Time  `json:"until"`
	Active   bool       `json:"active"`
	LiftedAt *time.Time `json:"liftedAt,omitempty"`
	LiftedBy *int64     `json:"liftedBy,omitempty"`
}

const (
	banAlertKind = "smb.address_banned"
	banRecent    = 7 * 24 * time.Hour // repeats within this make bans longer

	CodeBanNotFound errs.Code = "SMB_BAN_NOT_FOUND"
)

// Bans runs brute-force protection.
type Bans struct {
	db        *db.DB
	shares    Shares
	samba     Samba
	neighbors sys.Neighbors
	alerts    Alerts
	now       func() time.Time

	mu       sync.Mutex
	settings BanSettings
	allow    []netip.Prefix
	limiter  *ratelimit.Limiter
	tried    map[string][]string // address -> account names tried (last few)
}

// NewBans returns brute-force protection; Start loads its settings.
func NewBans(database *db.DB, s Shares, samba Samba, n sys.Neighbors, a Alerts) *Bans {
	return &Bans{db: database, shares: s, samba: samba, neighbors: n, alerts: a, now: time.Now, tried: map[string][]string{}}
}

func (b *Bans) Start(ctx context.Context) error {
	st, err := b.loadSettings(ctx)
	if err != nil {
		return err
	}
	b.use(st)
	return nil
}

func (b *Bans) use(st BanSettings) {
	var allow []netip.Prefix
	for _, a := range st.AllowList {
		if p, ok := prefix(a); ok {
			allow = append(allow, p)
		}
	}
	b.mu.Lock()
	b.settings, b.allow = st, allow
	b.limiter = ratelimit.New(st.Failures, time.Duration(st.WindowMinutes)*time.Minute)
	b.mu.Unlock()
}

func prefix(entry string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(entry); err == nil {
		return p, true
	}
	a, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// addressOf is what a client is counted and banned as: its IPv4 address,
// or its IPv6 /64.
func addressOf(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is4() {
		return ip.String()
	}
	return netip.PrefixFrom(ip, 64).Masked().String()
}

// exempt reports whether an address is never banned.
func (b *Bans) exempt(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsUnspecified() || !ip.IsValid() {
		return true
	}
	for _, p := range b.allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Run watches login events until the channel closes or ctx ends.
func (b *Bans) Run(ctx context.Context, events <-chan sambalog.Event) {
	work := context.WithoutCancel(ctx) // a ban in progress is finished
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Kind == sambalog.KindAuth {
				b.observe(work, ev)
			}
		}
	}
}

func (b *Bans) observe(ctx context.Context, ev sambalog.Event) {
	b.mu.Lock()
	enabled, follow := b.settings.Enabled, b.settings.FollowDevice
	exempt := b.exempt(ev.ClientIP)
	b.mu.Unlock()
	if !enabled || exempt {
		return
	}
	addr := addressOf(ev.ClientIP)

	// A banned device back with another address (DHCP): ban that too.
	if follow {
		if mac, ok := b.neighbors.MAC(ev.ClientIP); ok {
			if prev, ok := b.activeByMAC(ctx, mac); ok && prev.Address != addr {
				b.ban(ctx, ev.ClientIP, addr, mac, "same_device", 0, nil, &prev)
				return
			}
		}
	}

	b.mu.Lock()
	if ev.OK {
		b.limiter.Reset(addr) // a good login: start counting again
		delete(b.tried, addr)
		b.mu.Unlock()
		return
	}
	b.limiter.Fail(addr)
	if ev.User != "" && !slices.Contains(b.tried[addr], ev.User) && len(b.tried[addr]) < 10 {
		b.tried[addr] = append(b.tried[addr], ev.User)
	}
	blocked, _ := b.limiter.Blocked(addr)
	failures := b.settings.Failures
	tried := b.tried[addr]
	if blocked {
		b.limiter.Reset(addr)
		delete(b.tried, addr)
	}
	b.mu.Unlock()
	if !blocked {
		return
	}
	mac := ""
	if follow {
		mac, _ = b.neighbors.MAC(ev.ClientIP)
	}
	b.ban(ctx, ev.ClientIP, addr, mac, "failures", failures, tried, nil)
}

// ban records a ban, applies it and drops the address's connections.
func (b *Bans) ban(ctx context.Context, ip netip.Addr, addr, mac, reason string, failures int, tried []string, followed *Ban) {
	if b.isBanned(ctx, addr) {
		return
	}
	b.mu.Lock()
	st := b.settings
	b.mu.Unlock()
	now := b.now()
	offence, until := 1, time.Time{}
	if followed != nil {
		// The same device, the same offence: it keeps its time left.
		offence, until = followed.Offence, followed.Until
	} else {
		offence = b.recentOffences(ctx, addr, mac) + 1
		d := time.Duration(st.BanMinutes) * time.Minute
		for i := 1; i < offence && d < time.Duration(st.MaxBanHours)*time.Hour; i++ {
			d *= 4
		}
		until = now.Add(min(d, time.Duration(st.MaxBanHours)*time.Hour))
	}
	users, _ := json.Marshal(append([]string{}, tried...))
	res, err := b.db.Write.ExecContext(ctx, `INSERT INTO smb_bans (address, mac, reason, failures, users, offence, banned_at, until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, addr, mac, reason, failures, string(users), offence, now.Unix(), until.Unix())
	if err != nil {
		slog.ErrorContext(ctx, "brute-force protection: could not record a ban", "address", addr, "err", err)
		return
	}
	id, _ := res.LastInsertId()
	if err := b.shares.Reapply(ctx); err != nil {
		slog.ErrorContext(ctx, "brute-force protection: could not apply a ban", "address", addr, "err", err)
	}
	if err := b.samba.DisconnectClient(ctx, ip); err != nil {
		slog.WarnContext(ctx, "brute-force protection: could not disconnect", "ip", ip, "err", err)
	}
	slog.WarnContext(ctx, "address banned after failed SMB logins", "address", addr, "mac", mac, "reason", reason,
		"failures", failures, "offence", offence, "until", until, "ban", id)

	length := until.Sub(now).Round(time.Minute)
	var msg string
	if reason == "same_device" {
		msg = fmt.Sprintf("%s is a computer that was already banned (%s), back with a new address. Banned too, until %s.",
			addr, followed.Address, until.Local().Format("15:04 Jan 2"))
	} else {
		msg = fmt.Sprintf("%d failed SMB logins from %s within %d minutes. Banned for %s.", failures, addr, st.WindowMinutes, length)
		if len(tried) > 0 {
			msg += " Names tried: " + strings.Join(tried, ", ") + "."
		}
		if offence > 1 {
			msg += fmt.Sprintf(" This address or computer was banned %d times this week, so the ban is longer.", offence-1)
		}
	}
	if b.alerts != nil {
		_, _ = b.alerts.Raise(ctx, alerts.Raise{
			Kind: banAlertKind, Key: addr, Severity: alerts.Warning, Source: alerts.SourceSecurity,
			Title: "SMB logins blocked from " + addr, Message: msg,
			Details: map[string]any{"banId": id, "address": addr, "mac": mac, "reason": reason, "until": until, "users": tried},
		})
	}
}

func (b *Bans) isBanned(ctx context.Context, addr string) bool {
	var one int
	err := b.db.Read.QueryRowContext(ctx, "SELECT 1 FROM smb_bans WHERE address = ? AND lifted_at IS NULL AND until > ? LIMIT 1",
		addr, b.now().Unix()).Scan(&one)
	return err == nil
}

func (b *Bans) activeByMAC(ctx context.Context, mac string) (Ban, bool) {
	ban, err := scanBan(b.db.Read.QueryRowContext(ctx, "SELECT "+banColumns+` FROM smb_bans
		WHERE mac = ? AND mac != '' AND lifted_at IS NULL AND until > ? ORDER BY id DESC LIMIT 1`, mac, b.now().Unix()), b.now())
	return ban, err == nil
}

// recentOffences counts this address's or device's bans in the last week.
func (b *Bans) recentOffences(ctx context.Context, addr, mac string) int {
	var n int
	_ = b.db.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM smb_bans WHERE banned_at > ? AND reason = 'failures'
		AND (address = ? OR (mac != '' AND mac = ?))`, b.now().Add(-banRecent).Unix(), addr, mac).Scan(&n)
	return n
}

// --- for the shares module (the Samba config) ---

// BannedAddresses lists what Samba must refuse now.
func (b *Bans) BannedAddresses(ctx context.Context) ([]string, error) {
	rows, err := b.db.Read.QueryContext(ctx, `SELECT DISTINCT address FROM smb_bans WHERE lifted_at IS NULL AND until > ?
		ORDER BY banned_at DESC LIMIT ?`, b.now().Unix(), sys.MaxBans)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LoginAudit says whether Samba must log logins.
func (b *Bans) LoginAudit(context.Context) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings.Enabled, nil
}

// Expire ends bans whose time is up (a scheduled task, every minute).
func (b *Bans) Expire(ctx context.Context) error {
	now := b.now().Unix()
	rows, err := b.db.Read.QueryContext(ctx, "SELECT DISTINCT address FROM smb_bans WHERE lifted_at IS NULL AND until <= ?", now)
	if err != nil {
		return err
	}
	var ended []string
	for rows.Next() {
		var a string
		if rows.Scan(&a) == nil {
			ended = append(ended, a)
		}
	}
	rows.Close()
	if len(ended) == 0 {
		return nil
	}
	// Ended bans stay in the list (for the offence count); "lifted" is for
	// bans an admin lifted early.
	if _, err := b.db.Write.ExecContext(ctx, "UPDATE smb_bans SET lifted_at = until WHERE lifted_at IS NULL AND until <= ?", now); err != nil {
		return err
	}
	if b.alerts != nil {
		for _, a := range ended {
			if !b.isBanned(ctx, a) {
				_ = b.alerts.Resolve(ctx, banAlertKind, a)
			}
		}
	}
	slog.InfoContext(ctx, "SMB bans ended", "addresses", ended)
	return b.shares.Reapply(ctx)
}

// Purge forgets old failure counts (a scheduled task).
func (b *Bans) Purge(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limiter.Purge()
	return nil
}

// Lift ends a ban early (an admin's decision).
func (b *Bans) Lift(ctx context.Context, actor *httpx.Principal, id int64) (Ban, error) {
	res, err := b.db.Write.ExecContext(ctx, "UPDATE smb_bans SET lifted_at = ?, lifted_by = ? WHERE id = ? AND lifted_at IS NULL",
		b.now().Unix(), actor.UserID, id)
	if err != nil {
		return Ban{}, err
	}
	ban, err := b.get(ctx, id)
	if err != nil {
		return Ban{}, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if !b.isBanned(ctx, ban.Address) && b.alerts != nil {
			_ = b.alerts.Resolve(ctx, banAlertKind, ban.Address)
		}
		slog.InfoContext(ctx, "SMB ban lifted", "ban", id, "address", ban.Address, "by", actor.Username)
		if err := b.shares.Reapply(ctx); err != nil {
			return Ban{}, err
		}
	}
	return ban, nil
}

// --- settings ---

func (b *Bans) loadSettings(ctx context.Context) (BanSettings, error) {
	var st BanSettings
	var allow string
	var updated int64
	err := b.db.Read.QueryRowContext(ctx, `SELECT enabled, failures, window_minutes, ban_minutes, max_ban_hours, follow_device,
		allow_list, updated_at FROM smb_ban_settings WHERE id = 1`).
		Scan(&st.Enabled, &st.Failures, &st.WindowMinutes, &st.BanMinutes, &st.MaxBanHours, &st.FollowDevice, &allow, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return BanSettings{Enabled: true, Failures: 10, WindowMinutes: 10, BanMinutes: 15, MaxBanHours: 168, FollowDevice: true,
			AllowList: []string{}}, nil
	}
	if err != nil {
		return BanSettings{}, err
	}
	_ = json.Unmarshal([]byte(allow), &st.AllowList)
	if st.AllowList == nil {
		st.AllowList = []string{}
	}
	if updated > 0 {
		st.UpdatedAt = time.Unix(updated, 0).UTC()
	}
	return st, nil
}

func (b *Bans) Settings(ctx context.Context) (BanSettings, error) { return b.loadSettings(ctx) }

// UpdateSettings changes the settings. Bans of addresses now on the allow
// list are lifted at once; turning protection on or off changes Samba's
// login logging.
func (b *Bans) UpdateSettings(ctx context.Context, actor *httpx.Principal, in BanSettings) (BanSettings, error) {
	v := validate.New()
	allow := CheckBanSettings(v, in)
	if err := v.Err(); err != nil {
		return BanSettings{}, err
	}
	before, _ := b.loadSettings(ctx)
	raw, _ := json.Marshal(allow)
	if _, err := b.db.Write.ExecContext(ctx, `UPDATE smb_ban_settings SET enabled = ?, failures = ?, window_minutes = ?,
		ban_minutes = ?, max_ban_hours = ?, follow_device = ?, allow_list = ?, updated_at = ? WHERE id = 1`,
		in.Enabled, in.Failures, in.WindowMinutes, in.BanMinutes, in.MaxBanHours, in.FollowDevice, string(raw), b.now().Unix()); err != nil {
		return BanSettings{}, err
	}
	st, err := b.loadSettings(ctx)
	if err != nil {
		return BanSettings{}, err
	}
	b.use(st)

	// Lift bans the new allow list covers.
	lifted := 0
	if active, err := b.active(ctx); err == nil {
		for _, ban := range active {
			if p, ok := prefix(ban.Address); ok {
				b.mu.Lock()
				exempt := b.exempt(p.Addr())
				b.mu.Unlock()
				if exempt {
					if _, err := b.db.Write.ExecContext(ctx, "UPDATE smb_bans SET lifted_at = ?, lifted_by = ? WHERE id = ?",
						b.now().Unix(), actor.UserID, ban.ID); err == nil {
						lifted++
						if b.alerts != nil {
							_ = b.alerts.Resolve(ctx, banAlertKind, ban.Address)
						}
					}
				}
			}
		}
	}
	if lifted > 0 || before.Enabled != st.Enabled {
		if err := b.shares.Reapply(ctx); err != nil {
			return BanSettings{}, err
		}
	}
	slog.InfoContext(ctx, "SMB brute-force settings changed", "enabled", st.Enabled, "failures", st.Failures,
		"window_minutes", st.WindowMinutes, "allow_list", st.AllowList, "lifted", lifted, "by", actor.Username)
	return st, nil
}

// CheckBanSettings validates brute-force settings (the API's and a restored
// backup's) and returns the allow list normalized.
func CheckBanSettings(v *validate.Errors, in BanSettings) []string {
	v.Check(in.Failures >= 3 && in.Failures <= 1000, "failures", "out_of_range", "Between 3 (people mistype passwords) and 1000.")
	v.Check(in.WindowMinutes >= 1 && in.WindowMinutes <= 1440, "windowMinutes", "out_of_range", "Between 1 minute and a day.")
	v.Check(in.BanMinutes >= 1 && in.BanMinutes <= 10080, "banMinutes", "out_of_range", "Between 1 minute and a week.")
	v.Check(in.MaxBanHours >= 1 && in.MaxBanHours <= 8760 && in.MaxBanHours*60 >= in.BanMinutes, "maxBanHours", "too_small",
		"At least as long as the first ban, and at most a year.")
	if len(in.AllowList) > 64 {
		v.Add("allowList", "too_many", "At most 64 entries.")
		return nil
	}
	allow := make([]string, 0, len(in.AllowList))
	for i, a := range in.AllowList {
		c, err := sys.ParseHostRule(a)
		if err != nil {
			v.Add(fmt.Sprintf("allowList[%d]", i), "invalid_format", err.Error())
			continue
		}
		if !slices.Contains(allow, c) {
			allow = append(allow, c)
		}
	}
	return allow
}

// --- reading ---

const banColumns = "id, address, mac, reason, failures, users, offence, banned_at, until, lifted_at, lifted_by"

func scanBan(sc interface{ Scan(...any) error }, now time.Time) (Ban, error) {
	var ban Ban
	var users string
	var banned, until int64
	var lifted, by sql.NullInt64
	if err := sc.Scan(&ban.ID, &ban.Address, &ban.MAC, &ban.Reason, &ban.Failures, &users, &ban.Offence, &banned, &until,
		&lifted, &by); err != nil {
		return Ban{}, err
	}
	_ = json.Unmarshal([]byte(users), &ban.Users)
	if ban.Users == nil {
		ban.Users = []string{}
	}
	ban.BannedAt, ban.Until = time.Unix(banned, 0).UTC(), time.Unix(until, 0).UTC()
	if lifted.Valid {
		t := time.Unix(lifted.Int64, 0).UTC()
		ban.LiftedAt = &t
	}
	if by.Valid {
		ban.LiftedBy = &by.Int64
	}
	ban.Active = ban.LiftedAt == nil && ban.Until.After(now)
	return ban, nil
}

func (b *Bans) get(ctx context.Context, id int64) (Ban, error) {
	ban, err := scanBan(b.db.Read.QueryRowContext(ctx, "SELECT "+banColumns+" FROM smb_bans WHERE id = ?", id), b.now())
	if errors.Is(err, sql.ErrNoRows) {
		return Ban{}, errs.NotFound(CodeBanNotFound, "This ban does not exist.")
	}
	return ban, err
}

func (b *Bans) active(ctx context.Context) ([]Ban, error) {
	rows, err := b.db.Read.QueryContext(ctx, "SELECT "+banColumns+" FROM smb_bans WHERE lifted_at IS NULL AND until > ?", b.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		ban, err := scanBan(rows, b.now())
		if err != nil {
			return nil, err
		}
		out = append(out, ban)
	}
	return out, rows.Err()
}

// List returns bans, newest first; activeOnly leaves out ended ones.
func (b *Bans) List(ctx context.Context, activeOnly bool, p page.Request) ([]Ban, int, error) {
	where, args := "", []any{}
	if activeOnly {
		where, args = " WHERE lifted_at IS NULL AND until > ?", []any{b.now().Unix()}
	}
	var total int
	if err := b.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM smb_bans"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.Read.QueryContext(ctx, "SELECT "+banColumns+" FROM smb_bans"+where+" ORDER BY id DESC LIMIT ? OFFSET ?",
		append(args, p.Size, p.Offset())...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Ban{}
	for rows.Next() {
		ban, err := scanBan(rows, b.now())
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ban)
	}
	return out, total, rows.Err()
}
