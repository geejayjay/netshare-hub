package replication

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

// ReplicationJobRequest creates a job.
type ReplicationJobRequest struct {
	Name         string   `json:"name" minLength:"1" maxLength:"64"`
	ShareID      int64    `json:"shareId" minimum:"1"`
	LocalPath    string   `json:"localPath,omitempty" maxLength:"1024" doc:"A folder on this machine (a second disk)."`
	PeerID       int64    `json:"peerId,omitempty" doc:"A paired NetShare Hub."`
	Mirror       bool     `json:"mirror,omitempty" doc:"Delete files at the destination that are gone from the share."`
	BandwidthKiB int      `json:"bandwidthKiB,omitempty" minimum:"0"`
	Excludes     []string `json:"excludes,omitempty" maxItems:"64"`
	Schedule     string   `json:"schedule,omitempty" enum:"manual,hourly,daily,weekly"`
	AtMinute     int      `json:"atMinute,omitempty" minimum:"0" maximum:"1439"`
	Weekday      int      `json:"weekday,omitempty" minimum:"0" maximum:"6"`
	Enabled      *bool    `json:"enabled,omitempty" doc:"Default true."`
}

// ReplicationJobPatch changes a job's fields that are present.
type ReplicationJobPatch struct {
	Name         *string   `json:"name,omitempty" maxLength:"64"`
	LocalPath    *string   `json:"localPath,omitempty" maxLength:"1024"`
	PeerID       *int64    `json:"peerId,omitempty"`
	Mirror       *bool     `json:"mirror,omitempty"`
	BandwidthKiB *int      `json:"bandwidthKiB,omitempty" minimum:"0"`
	Excludes     *[]string `json:"excludes,omitempty" maxItems:"64"`
	Schedule     *string   `json:"schedule,omitempty" enum:"manual,hourly,daily,weekly"`
	AtMinute     *int      `json:"atMinute,omitempty" minimum:"0" maximum:"1439"`
	Weekday      *int      `json:"weekday,omitempty" minimum:"0" maximum:"6"`
	Enabled      *bool     `json:"enabled,omitempty"`
}

// ReplicationPeerRequest pairs with another NetShare Hub.
type ReplicationPeerRequest struct {
	Name    string `json:"name" minLength:"1" maxLength:"64"`
	URL     string `json:"url" maxLength:"300" doc:"The other NetShare Hub's web address, like http://192.168.1.20:8080."`
	Code    string `json:"code" maxLength:"64" doc:"The pairing code made on the other NetShare Hub (Replication, Receiving folders)."`
	SSHPort int    `json:"sshPort,omitempty" minimum:"0" maximum:"65535" doc:"The other machine's SSH port (default 22)."`
}

// ReplicationReceiverRequest adds a receiving folder.
type ReplicationReceiverRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"64"`
	Path string `json:"path" maxLength:"1024" doc:"A new folder for the copies, like /srv/replicas/office. It may not be or hold a share."`
}

// ReplicationReceiverCode is a receiving folder and its pairing code (shown once).
type ReplicationReceiverCode struct {
	Receiver ReplicationReceiver `json:"receiver"`
	Code     string              `json:"code" doc:"Enter it on the sending NetShare Hub within 15 minutes. It works once."`
}

type (
	idInput struct {
		ID int64 `path:"id" minimum:"1"`
	}
	jobInput struct {
		Body ReplicationJobRequest
	}
	jobPatchInput struct {
		ID   int64 `path:"id" minimum:"1"`
		Body ReplicationJobPatch
	}
	jobOutput struct {
		Body ReplicationJob
	}
	jobsOutput struct {
		Body page.List[ReplicationJob]
	}
	runsOutput struct {
		Body page.List[ReplicationRun]
	}
	taskOutput struct {
		Body jobs.Job
	}
	statusOutput struct {
		Body ReplicationStatus
	}
	peerInput struct {
		Body ReplicationPeerRequest
	}
	peerOutput struct {
		Body ReplicationPeer
	}
	peersOutput struct {
		Body page.List[ReplicationPeer]
	}
	receiverInput struct {
		Body ReplicationReceiverRequest
	}
	receiverCodeOutput struct {
		Body ReplicationReceiverCode
	}
	receiversOutput struct {
		Body page.List[ReplicationReceiver]
	}
	codeOutput struct {
		Body struct {
			Code string `json:"code"`
		}
	}
	pairInput struct {
		Body ReplicationPairRequest
	}
	pairOutput struct {
		Body ReplicationPairResponse
	}
)

// Register adds the routes. Everything needs replication:manage (Admin),
// except POST /replication/pair, which the other NetShare Hub calls with a
// pairing code instead of a session.
func Register(api huma.API, s *Service) {
	const tag = "Replication"
	manage := httpx.Require(rbac.ReplicationManage)
	route := func(id, method, path, summary string, status int) httpx.Route {
		return httpx.Route{ID: id, Method: method, Path: path, Summary: summary, Tag: tag, Access: manage, Status: status}
	}

	httpx.Register(api, route("getReplicationStatus", http.MethodGet, "/replication/status", "What this machine can do", 0),
		func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
			st, err := s.Status(ctx)
			return &statusOutput{Body: st}, err
		})

	httpx.Register(api, route("listReplicationJobs", http.MethodGet, "/replication/jobs", "List replication jobs", 0),
		func(ctx context.Context, _ *struct{}) (*jobsOutput, error) {
			list, err := s.ListJobs(ctx)
			if err != nil {
				return nil, err
			}
			return &jobsOutput{Body: page.All(list)}, nil
		})
	httpx.Register(api, route("createReplicationJob", http.MethodPost, "/replication/jobs", "Create a replication job", http.StatusCreated),
		func(ctx context.Context, in *jobInput) (*jobOutput, error) {
			b := in.Body
			enabled := b.Enabled == nil || *b.Enabled
			j, err := s.CreateJob(ctx, httpx.PrincipalFrom(ctx).UserID, ReplicationJob{Name: b.Name, ShareID: b.ShareID, LocalPath: b.LocalPath,
				PeerID: b.PeerID, Mirror: b.Mirror, BandwidthKiB: b.BandwidthKiB, Excludes: b.Excludes, Schedule: b.Schedule,
				AtMinute: b.AtMinute, Weekday: b.Weekday, Enabled: enabled})
			return &jobOutput{Body: j}, err
		})
	httpx.Register(api, route("getReplicationJob", http.MethodGet, "/replication/jobs/{id}", "Get a replication job", 0),
		func(ctx context.Context, in *idInput) (*jobOutput, error) {
			j, err := s.GetJob(ctx, in.ID)
			return &jobOutput{Body: j}, err
		})
	httpx.Register(api, route("updateReplicationJob", http.MethodPatch, "/replication/jobs/{id}", "Change a replication job", 0),
		func(ctx context.Context, in *jobPatchInput) (*jobOutput, error) {
			b := in.Body
			j, err := s.UpdateJob(ctx, in.ID, JobUpdate{Name: b.Name, LocalPath: b.LocalPath, PeerID: b.PeerID, Mirror: b.Mirror,
				BandwidthKiB: b.BandwidthKiB, Excludes: b.Excludes, Schedule: b.Schedule, AtMinute: b.AtMinute, Weekday: b.Weekday,
				Enabled: b.Enabled})
			return &jobOutput{Body: j}, err
		})
	httpx.Register(api, route("deleteReplicationJob", http.MethodDelete, "/replication/jobs/{id}",
		"Delete a replication job (the copies are kept)", http.StatusNoContent),
		func(ctx context.Context, in *idInput) (*struct{}, error) { return nil, s.DeleteJob(ctx, in.ID) })
	httpx.Register(api, route("runReplicationJob", http.MethodPost, "/replication/jobs/{id}/run", "ReplicationRun a job now (a background job)",
		http.StatusAccepted),
		func(ctx context.Context, in *idInput) (*taskOutput, error) {
			j, err := s.Start(ctx, httpx.PrincipalFrom(ctx).UserID, in.ID, false)
			return &taskOutput{Body: j}, err
		})
	httpx.Register(api, route("previewReplicationJob", http.MethodPost, "/replication/jobs/{id}/preview",
		"Preview what a run would change (a dry run; nothing is changed)", http.StatusAccepted),
		func(ctx context.Context, in *idInput) (*taskOutput, error) {
			j, err := s.Start(ctx, httpx.PrincipalFrom(ctx).UserID, in.ID, true)
			return &taskOutput{Body: j}, err
		})
	httpx.Register(api, route("listReplicationRuns", http.MethodGet, "/replication/jobs/{id}/runs", "A job's runs, newest first", 0),
		func(ctx context.Context, in *idInput) (*runsOutput, error) {
			list, err := s.Runs(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			return &runsOutput{Body: page.All(list)}, nil
		})

	httpx.Register(api, route("listReplicationPeers", http.MethodGet, "/replication/peers", "List paired NetShare Hubs", 0),
		func(ctx context.Context, _ *struct{}) (*peersOutput, error) {
			list, err := s.Peers(ctx)
			if err != nil {
				return nil, err
			}
			return &peersOutput{Body: page.All(list)}, nil
		})
	httpx.Register(api, route("addReplicationPeer", http.MethodPost, "/replication/peers",
		"Pair with another NetShare Hub (with a code made there)", http.StatusCreated),
		func(ctx context.Context, in *peerInput) (*peerOutput, error) {
			p, err := s.AddPeer(ctx, PeerInput{Name: in.Body.Name, URL: in.Body.URL, Code: in.Body.Code, SSHPort: in.Body.SSHPort})
			return &peerOutput{Body: p}, err
		})
	httpx.Register(api, route("deleteReplicationPeer", http.MethodDelete, "/replication/peers/{id}",
		"Forget a peer (and its jobs)", http.StatusNoContent),
		func(ctx context.Context, in *idInput) (*struct{}, error) { return nil, s.DeletePeer(ctx, in.ID) })

	httpx.Register(api, route("listReplicationReceivers", http.MethodGet, "/replication/receivers", "List receiving folders", 0),
		func(ctx context.Context, _ *struct{}) (*receiversOutput, error) {
			list, err := s.Receivers(ctx)
			if err != nil {
				return nil, err
			}
			return &receiversOutput{Body: page.All(list)}, nil
		})
	httpx.Register(api, route("addReplicationReceiver", http.MethodPost, "/replication/receivers",
		"Add a receiving folder and get its pairing code", http.StatusCreated),
		func(ctx context.Context, in *receiverInput) (*receiverCodeOutput, error) {
			rc, code, err := s.AddReceiver(ctx, in.Body.Name, in.Body.Path)
			return &receiverCodeOutput{Body: ReplicationReceiverCode{Receiver: rc, Code: code}}, err
		})
	httpx.Register(api, route("newReplicationPairingCode", http.MethodPost, "/replication/receivers/{id}/pairing-code",
		"Make a new pairing code (pairing again replaces the sender)", 0),
		func(ctx context.Context, in *idInput) (*codeOutput, error) {
			out := &codeOutput{}
			var err error
			out.Body.Code, err = s.NewPairingCode(ctx, in.ID)
			return out, err
		})
	httpx.Register(api, route("deleteReplicationReceiver", http.MethodDelete, "/replication/receivers/{id}",
		"Stop receiving (the copies are kept)", http.StatusNoContent),
		func(ctx context.Context, in *idInput) (*struct{}, error) { return nil, s.DeleteReceiver(ctx, in.ID) })

	httpx.Register(api, httpx.Route{ID: "pairReplication", Method: http.MethodPost, Path: "/replication/pair",
		Summary: "Pair a sender (called by the other NetShare Hub, with a pairing code)", Tag: tag, Access: httpx.Public()},
		func(ctx context.Context, in *pairInput) (*pairOutput, error) {
			res, err := s.Pair(ctx, in.Body)
			return &pairOutput{Body: res}, err
		})
}
