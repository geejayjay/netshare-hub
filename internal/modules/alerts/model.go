// Package alerts is ONE alert model for the whole application: everything an
// admin must see (a share switched to read-only by the ransomware guard, a
// banned IP, Samba not reloading, a disk almost full). Features raise
// alerts; they never build their own notifications. Alerts are stored, so
// they survive a refresh and show on every device, and are sent live to
// the admins over the events channel.
//
// An alert is identified by its kind and key ("ip.banned" + "192.168.1.66").
// Raising one that is already active counts it up instead of adding
// another, so a burst never floods the list. It stays active until it is
// resolved (the cause went away: the feature calls Resolve) or acknowledged
// (an admin saw it).
package alerts

import (
	"encoding/json"
	"time"

	"netshare-hub/internal/platform/errs"
)

// Severity orders alerts. Critical ones get a sticky banner in the UI.
type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Error    Severity = "error"
	Critical Severity = "critical"
)

func (s Severity) valid() bool { return s == Info || s == Warning || s == Error || s == Critical }

// Source groups alerts for the UI's filters.
type Source string

const (
	SourceShares      Source = "shares"
	SourceUploads     Source = "uploads"
	SourceSecurity    Source = "security"
	SourceStorage     Source = "storage"
	SourceReplication Source = "replication"
	SourceSystem      Source = "system"
)

func (s Source) valid() bool {
	switch s {
	case SourceShares, SourceUploads, SourceSecurity, SourceStorage, SourceReplication, SourceSystem:
		return true
	}
	return false
}

// Alert is what admins see.
type Alert struct {
	ID       int64           `json:"id"`
	Kind     string          `json:"kind" doc:"What happened, e.g. \"samba.reload_failed\". Stable: the UI may switch on it."`
	Key      string          `json:"key" doc:"Which one (a share, an IP...); \"\" when there is only one."`
	Severity Severity        `json:"severity" enum:"info,warning,error,critical"`
	Source   Source          `json:"source" enum:"shares,uploads,security,storage,replication,system"`
	Title    string          `json:"title"`
	Message  string          `json:"message" doc:"What happened and what to do, for a person."`
	Details  json.RawMessage `json:"details" doc:"Machine-readable facts for the UI (affected files, counts...)."`
	Count    int             `json:"count" doc:"How many times it happened while active."`
	FirstAt  time.Time       `json:"firstAt"`
	LastAt   time.Time       `json:"lastAt"`
	Active   bool            `json:"active" doc:"Neither resolved nor acknowledged."`
	// ResolvedAt is set when the cause went away by itself.
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	// AcknowledgedAt and AcknowledgedBy are set when an admin dismissed it.
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty"`
	AcknowledgedBy *int64     `json:"acknowledgedBy,omitempty"`
}

// Raise is what a feature reports.
type Raise struct {
	Kind     string
	Key      string
	Severity Severity
	Source   Source
	Title    string
	Message  string
	// Details is any JSON-encodable value (nil for none). Keep it small.
	Details any
}

// Error codes owned by this module.
const (
	CodeAlertNotFound errs.Code = "ALERT_NOT_FOUND"
)

func errNotFound() error { return errs.NotFound(CodeAlertNotFound, "This alert does not exist.") }
