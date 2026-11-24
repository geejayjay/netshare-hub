package replication

import (
	"testing"
	"time"
)

func TestNextRun(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name string
		job  ReplicationJob
		now  string
		want string // "" = none
	}{
		{"manual", ReplicationJob{Enabled: true, Schedule: ScheduleManual}, "2026-11-24 10:00", ""},
		{"disabled", ReplicationJob{Enabled: false, Schedule: ScheduleDaily}, "2026-11-24 10:00", ""},
		{"hourly later this hour", ReplicationJob{Enabled: true, Schedule: ScheduleHourly, AtMinute: 30}, "2026-11-24 10:10", "2026-11-24 10:30"},
		{"hourly next hour", ReplicationJob{Enabled: true, Schedule: ScheduleHourly, AtMinute: 30}, "2026-11-24 10:30", "2026-11-24 11:30"},
		{"daily today", ReplicationJob{Enabled: true, Schedule: ScheduleDaily, AtMinute: 23 * 60}, "2026-11-24 10:00", "2026-11-24 23:00"},
		{"daily tomorrow", ReplicationJob{Enabled: true, Schedule: ScheduleDaily, AtMinute: 2*60 + 30}, "2026-11-24 10:00", "2026-11-25 02:30"},
		{"daily across a month", ReplicationJob{Enabled: true, Schedule: ScheduleDaily, AtMinute: 60}, "2026-11-30 22:00", "2026-12-01 01:00"},
		// 2026-11-24 is a Tuesday.
		{"weekly sunday", ReplicationJob{Enabled: true, Schedule: ScheduleWeekly, Weekday: 0, AtMinute: 3 * 60}, "2026-11-24 10:00", "2026-11-29 03:00"},
		{"weekly same day later", ReplicationJob{Enabled: true, Schedule: ScheduleWeekly, Weekday: 2, AtMinute: 20 * 60}, "2026-11-24 10:00", "2026-11-24 20:00"},
		{"weekly same day passed", ReplicationJob{Enabled: true, Schedule: ScheduleWeekly, Weekday: 2, AtMinute: 9 * 60}, "2026-11-24 10:00", "2026-12-01 09:00"},
	}
	for _, c := range cases {
		got := nextRun(c.job, at(c.now))
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: got %v, want none", c.name, got)
		case c.want != "" && (got == nil || !got.Equal(at(c.want))):
			t.Errorf("%s: got %v, want %s", c.name, got, c.want)
		}
	}
}

func TestPairMAC(t *testing.T) {
	// Every part is bound: changing any of them changes the MAC.
	base := pairMAC("secret", pairLabel, "r1", "nonce", "key")
	for _, other := range []string{
		pairMAC("secret2", pairLabel, "r1", "nonce", "key"),
		pairMAC("secret", pairLabel, "r2", "nonce", "key"),
		pairMAC("secret", pairLabel, "r1", "nonce2", "key"),
		pairMAC("secret", pairLabel, "r1", "nonce", "key2"),
		pairMAC("secret", pairedLabl, "r1", "nonce", "key"),
		// Parts are separated: moving text between them is not the same.
		pairMAC("secret", pairLabel, "r1", "nonc", "ekey"),
	} {
		if other == base {
			t.Error("a different exchange has the same MAC")
		}
	}
}
