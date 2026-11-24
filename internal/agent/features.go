package agent

// Features are the optional groups of operations. The agent only offers
// the operations of the modules switched on in the config (it reads the
// same file as the web server): a module that is off is not a smaller
// feature but a smaller root surface, because its operations do not exist.
type Features struct {
	FileManager bool // files.worker
	SMBEvents   bool // samba.audit-events (activity log, guard, brute force)
	Disconnect  bool // samba.disconnect-client (guard, brute force)
	Sessions    bool // samba.sessions, samba.disconnect-session
	Discovery   bool // discovery.*
	Backup      bool // accounts.smb-hashes, accounts.create-from-hash
	NFS         bool // nfs.*
	Replication bool // replication.*
	Block       bool // block.*
}

// AllFeatures offers every operation.
func AllFeatures() Features {
	return Features{FileManager: true, SMBEvents: true, Disconnect: true, Sessions: true, Discovery: true, Backup: true, NFS: true,
		Replication: true, Block: true}
}

// offers reports whether an operation belongs to a feature that is on.
// Core operations (accounts, Samba config, share folders) are always on.
func (f Features) offers(op string) bool {
	switch op {
	case opWorker:
		return f.FileManager
	case opAuditEvents:
		return f.SMBEvents
	case opDisconnectClient:
		return f.Disconnect
	case opSessions, opDisconnectSession:
		return f.Sessions
	case opDiscoveryStatus, opSetDiscovery:
		return f.Discovery
	case opSMBHashes, opCreateFromHash:
		return f.Backup
	case opNFSStatus, opApplyExports:
		return f.NFS
	case opReplicationStatus, opReplicate, opPublicKey, opSetPeers, opHostKey, opSetReceivers:
		return f.Replication
	case opBlockStatus, opBlockDevices, opApplyDisks:
		return f.Block
	}
	return true
}
