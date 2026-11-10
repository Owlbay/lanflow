package syncfile

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ICloudSubdir is Lanflow's folder inside iCloud Drive.
const ICloudSubdir = "Lanflow"

// ICloudDrive returns iCloud Drive's local folder under home
// (~/Library/Mobile Documents/com~apple~CloudDocs); it exists only on a
// Mac signed in to iCloud with iCloud Drive on.
func ICloudDrive(home string) string {
	return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
}

// PlaceholderName is the name iCloud gives a file it has not downloaded
// yet: ".lanflow.yaml.icloud" beside where lanflow.yaml will be.
func PlaceholderName(name string) string { return "." + name + ".icloud" }

// conflictPattern matches the copies sync services leave when two
// machines changed the file at once:
//
//	lanflow 2.yaml                                   iCloud Drive
//	lanflow (1).yaml, lanflow (… conflicted copy …).yaml   Dropbox, Google Drive
//	lanflow-DESKTOP-1AB2C3D.yaml                     OneDrive
//	lanflow.sync-conflict-20260102-150405-ABCDEFG.yaml   Syncthing
var conflictPattern = regexp.MustCompile(`^lanflow( \d+| ?\(.+\)|-[A-Za-z0-9][A-Za-z0-9-]*|\.sync-conflict-.+)\.yaml$`)

// IsConflictCopy reports whether name (a file name in the sync folder) is
// a conflict copy of lanflow.yaml; iCloud placeholders of copies count.
func IsConflictCopy(name string) bool {
	if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".icloud") {
		name = strings.TrimSuffix(strings.TrimPrefix(name, "."), ".icloud")
	}
	return conflictPattern.MatchString(name)
}

// Snapshot is what the folder looks like at one moment.
type Snapshot struct {
	Exists      bool      // lanflow.yaml is there (downloaded)
	Placeholder bool      // only iCloud's placeholder is there
	Size        int64     // of lanflow.yaml
	ModTime     time.Time // of lanflow.yaml
	Conflicts   []string  // conflict copies, by name, sorted
}

// Same reports whether two snapshots show the same file.
func (a Snapshot) Same(b Snapshot) bool {
	return a.Exists == b.Exists && a.Placeholder == b.Placeholder && a.Size == b.Size && a.ModTime.Equal(b.ModTime) &&
		strings.Join(a.Conflicts, "\x00") == strings.Join(b.Conflicts, "\x00")
}

// Look takes a snapshot of dir.
func Look(dir string) Snapshot {
	var s Snapshot
	if fi, err := os.Stat(filepath.Join(dir, FileName)); err == nil && fi.Mode().IsRegular() {
		s.Exists, s.Size, s.ModTime = true, fi.Size(), fi.ModTime()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s
	}
	for _, e := range entries {
		switch name := e.Name(); {
		case name == PlaceholderName(FileName):
			s.Placeholder = !s.Exists
		case IsConflictCopy(name):
			s.Conflicts = append(s.Conflicts, name)
		}
	}
	sort.Strings(s.Conflicts)
	return s
}
