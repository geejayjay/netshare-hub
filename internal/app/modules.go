package app

import (
	"netshare-hub/internal/agent"
	"netshare-hub/internal/modules/system"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys"
)

// TrimSystem leaves out the parts of the system layer that belong to
// modules that are off, so nothing can reach them by accident. The agent
// trims its own copy the same way (it reads the same config).
func TrimSystem(s sys.System, m config.Modules) sys.System {
	if !m.FileManager {
		s.Workers = nil
	}
	if !m.SMBEvents() {
		s.Audit = nil
	}
	if !m.BruteForce {
		s.Neighbors = nil
	}
	if !m.Discovery {
		s.Discovery = nil
	}
	if !m.NFS {
		s.NFS = nil
	}
	if !m.Replication {
		s.Replication = nil
	}
	if !m.BlockStorage {
		s.Block = nil
	}
	return s
}

// AgentFeatures is which agent operations the modules need.
func AgentFeatures(m config.Modules) agent.Features {
	return agent.Features{
		FileManager: m.FileManager,
		SMBEvents:   m.SMBEvents(),
		Disconnect:  m.RansomwareGuard || m.BruteForce,
		Sessions:    m.Sessions,
		Discovery:   m.Discovery,
		Backup:      m.Backup,
		NFS:         m.NFS,
		Replication: m.Replication,
		Block:       m.BlockStorage,
	}
}

// moduleList is the modules' state for GET /system/modules.
func moduleList(m config.Modules) system.Modules {
	return system.Modules{
		FileManager: m.FileManager, ActivityLog: m.ActivityLog, RansomwareGuard: m.RansomwareGuard,
		BruteForce: m.BruteForce, Discovery: m.Discovery, Sessions: m.Sessions, Backup: m.Backup,
		NFS: m.NFS, Replication: m.Replication, BlockStorage: m.BlockStorage,
	}
}
