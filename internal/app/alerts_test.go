package app_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/sys"
)

// Step 2.1: one alert model (raised by features, counted up, resolved by
// itself or acknowledged), and one scheduler.

type alertBody struct {
	ID       int64
	Kind     string
	Severity string
	Source   string
	Title    string
	Count    int
	Active   bool
}

func activeAlerts(t *testing.T, c *client) []alertBody {
	t.Helper()
	var list struct{ Items []alertBody }
	if status, _ := c.do("GET", "/alerts?active=true", nil, &list); status != 200 {
		t.Fatalf("list alerts: %d", status)
	}
	return list.Items
}

func TestSambaReloadFailureIsAnAlert(t *testing.T) {
	admin, fake, base := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	var sh shareBody
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	if got := activeAlerts(t, admin); len(got) != 0 {
		t.Fatalf("alerts before anything went wrong: %+v", got)
	}

	// Samba does not reload, twice: one alert, counted twice.
	for i := 0; i < 2; i++ {
		fake.FailNext("ApplyConfig", sys.ErrReloadFailed)
		admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"comment": "try " + itoa(int64(i))}, nil)
	}
	got := activeAlerts(t, admin)
	if len(got) != 1 || got[0].Kind != "samba.reload_failed" || got[0].Count != 2 || got[0].Severity != "error" || got[0].Source != "shares" {
		t.Fatalf("alerts: %+v", got)
	}

	// The next change that reloads fine resolves it.
	admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"comment": "fine"}, nil)
	if got := activeAlerts(t, admin); len(got) != 0 {
		t.Errorf("alert still active after Samba reloaded: %+v", got)
	}
	var all struct{ Items []alertBody }
	admin.do("GET", "/alerts", nil, &all)
	if len(all.Items) != 1 || all.Items[0].Active {
		t.Errorf("the resolved alert stays in the list, inactive: %+v", all.Items)
	}

	// It happens again: a new alert, which an admin acknowledges.
	fake.FailNext("ApplyConfig", sys.ErrReloadFailed)
	admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"comment": "again"}, nil)
	got = activeAlerts(t, admin)
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("a new occurrence after resolving: %+v", got)
	}
	var acked struct {
		Active         bool
		AcknowledgedBy *int64
	}
	status, _ := admin.do("POST", "/alerts/"+itoa(got[0].ID)+"/acknowledge", nil, &acked)
	want(t, "acknowledge", status, 200)
	if acked.Active || acked.AcknowledgedBy == nil {
		t.Errorf("acknowledged: %+v", acked)
	}
	if len(activeAlerts(t, admin)) != 0 {
		t.Error("an acknowledged alert is still active")
	}

	// Who may see and act on alerts.
	plain := newClient(t, base)
	plain.login("jay", pw)
	if status, _ := plain.do("GET", "/alerts", nil, nil); status != 403 {
		t.Errorf("a plain user listed alerts: %d", status)
	}
	if status, _ := plain.do("POST", "/alerts/"+itoa(got[0].ID)+"/acknowledge", nil, nil); status != 403 {
		t.Errorf("a plain user acknowledged an alert: %d", status)
	}
	status, _ = admin.do("DELETE", "/alerts/"+itoa(got[0].ID), nil, nil)
	want(t, "delete", status, 204)
	status, _ = admin.do("GET", "/alerts/"+itoa(got[0].ID), nil, nil)
	want(t, "deleted alert", status, 404)
}

// Alerts go live to admins over the events channel, and only to them.
func TestAlertsAreSentLiveToAdminsOnly(t *testing.T) {
	admin, fake, base := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	var sh shareBody
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	plain := newClient(t, base)
	plain.login("jay", pw)

	listen := func(c *client) <-chan string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/events", nil)
		res, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("events: status %d (the channel must stream through the middleware)", res.StatusCode)
		}
		got := make(chan string, 16)
		go func() {
			defer res.Body.Close()
			sc := bufio.NewScanner(res.Body)
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "event: ") {
					got <- strings.TrimPrefix(sc.Text(), "event: ")
				}
			}
			close(got)
		}()
		return got
	}
	adminEvents, plainEvents := listen(admin), listen(plain)
	time.Sleep(100 * time.Millisecond)

	fake.FailNext("ApplyConfig", sys.ErrReloadFailed)
	admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"comment": "x"}, nil)

	seen := func(events <-chan string) bool {
		timeout := time.After(1500 * time.Millisecond)
		for {
			select {
			case e, ok := <-events:
				if !ok {
					return false
				}
				if e == "alert" {
					return true
				}
			case <-timeout:
				return false
			}
		}
	}
	if !seen(adminEvents) {
		t.Error("the admin did not get the alert live")
	}
	if seen(plainEvents) {
		t.Error("a plain user got an alert")
	}
}

func TestScheduledTasksAreListed(t *testing.T) {
	admin, _, _ := newSystemServer(t)
	var list struct {
		Items []struct{ Name, Schedule string }
	}
	status, _ := admin.do("GET", "/system/tasks", nil, &list)
	want(t, "status", status, 200)
	var names []string
	for _, task := range list.Items {
		names = append(names, task.Name+" ("+task.Schedule+")")
	}
	want(t, "tasks", strings.Join(names, ", "),
		"activity.cleanup (daily at 04:00), alerts.cleanup (daily at 03:30), jobs.cleanup (every 1h0m0s), "+
			"search.reindex (daily at 03:00), shares.recycle-purge (daily at 02:45), smb-bans.expire (every 1m0s), "+
			"smb-bans.forget (every 10m0s), storage.scan (daily at 04:30), thumbs.cleanup (daily at 05:00), uploads.cleanup (every 1h0m0s)")
}
