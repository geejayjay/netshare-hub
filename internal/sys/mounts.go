package sys

// Mount warning codes reported by Files.InspectMounts.
const (
	MountWarnReadOnly    = "read_only_mount"
	MountWarnRootDisk    = "on_root_disk"
	MountWarnUIDMismatch = "uid_mapping_mismatch"
	MountWarnMissing     = "path_missing"
)

// MaxMountPaths is the most folders one InspectMounts call checks.
const MaxMountPaths = 500

// MountCheck summarizes mount health for a single directory path.
type MountCheck struct {
	Path        string   `json:"path"`
	MountPoint  string   `json:"mountPoint"`
	FSType      string   `json:"fsType,omitempty"`
	ReadOnly    bool     `json:"readOnly"`
	OnRootDisk  bool     `json:"onRootDisk"`
	UIDMismatch bool     `json:"uidMismatch"`
	Warnings    []string `json:"warnings"`
}

// MountReport describes the container environment and checked share paths.
type MountReport struct {
	Environment  string       `json:"environment" enum:"host,lxc,docker"`
	Unprivileged bool         `json:"unprivileged"`
	Checks       []MountCheck `json:"checks"`
}
