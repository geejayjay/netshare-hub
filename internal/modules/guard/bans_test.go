package guard

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/sys/sambalog"
)

type countShares struct{ n int }

func (c *countShares) Reapply(context.Context) error { c.n++; return nil }

type noSamba struct{}

func (noSamba) DisconnectClient(context.Context, netip.Addr) error { return nil }

type noNeighbors struct{}

func (noNeighbors) MAC(netip.Addr) (string, bool) { return "", false }

func TestBansEndOnTime(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(context.Background(), migrations.FS, ""); err != nil {
		t.Fatal(err)
	}
	shares := &countShares{}
	b := NewBans(d, shares, noSamba{}, noNeighbors{}, nil)
	now := time.Now()
	b.now = func() time.Time { return now }
	ctx := context.Background()
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("192.168.1.66")
	for i := 0; i < 10; i++ {
		b.observe(ctx, sambalog.Event{Kind: sambalog.KindAuth, ClientIP: ip, User: "admin", AuthStatus: "NT_STATUS_WRONG_PASSWORD"})
	}
	if got, _ := b.BannedAddresses(ctx); len(got) != 1 || got[0] != "192.168.1.66" {
		t.Fatalf("banned: %v", got)
	}

	// Not yet.
	now = now.Add(14 * time.Minute)
	before := shares.n
	b.Expire(ctx)
	if got, _ := b.BannedAddresses(ctx); len(got) != 1 || shares.n != before {
		t.Fatalf("ended early: %v", got)
	}
	// Now: gone from Samba's list, and the config is written again.
	now = now.Add(2 * time.Minute)
	b.Expire(ctx)
	if got, _ := b.BannedAddresses(ctx); len(got) != 0 || shares.n != before+1 {
		t.Errorf("after the ban time: %v (config written %d times)", got, shares.n-before)
	}
	// The ended ban still counts as an offence: the next one is longer.
	for i := 0; i < 10; i++ {
		b.observe(ctx, sambalog.Event{Kind: sambalog.KindAuth, ClientIP: ip, User: "admin", AuthStatus: "NT_STATUS_WRONG_PASSWORD"})
	}
	bans, _, _ := b.List(ctx, true, pageOf(10))
	if len(bans) != 1 || bans[0].Offence != 2 || bans[0].Until.Sub(bans[0].BannedAt) != time.Hour {
		t.Errorf("second ban: %+v", bans)
	}
}

func pageOf(n int) page.Request { return page.Request{Size: n, Page: 1} }
