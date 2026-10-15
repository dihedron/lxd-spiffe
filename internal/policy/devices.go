package policy

import (
	"path"
	"slices"
	"strings"
)

// Relation is how a device's path relates to the proof directory.
type Relation string

// Relations of a device to the proof directory. Equal, Ancestor, Below and
// NotAbsolute reject; the others are information.
const (
	Equal       Relation = "equal"        // a disk mounted at the proof directory
	Ancestor    Relation = "ancestor"     // a disk mounted above it
	NotAbsolute Relation = "not-absolute" // a disk path that is not absolute: fail closed
	Below       Relation = "below"        // a disk mounted under it
	NoPath      Relation = "no-path"      // a disk with no mount path (a block device)
	OtherType   Relation = "other-type"   // a device of another type that has a path
)

// DeviceFinding is one device the isolation check reports.
type DeviceFinding struct {
	Device   string   `json:"device"`
	Type     string   `json:"type"`
	Path     string   `json:"path,omitempty"`
	Source   string   `json:"source,omitempty"`
	Relation Relation `json:"relation"`
}

// Isolation is the result of CheckIsolation.
type Isolation struct {
	Reject bool            `json:"reject"`
	Causes []DeviceFinding `json:"causes"`
	Info   []DeviceFinding `json:"info"`
}

// CheckIsolation is the isolation check of the attestor spec (Proof of
// co-location, step 5) on an instance's expanded_devices: it rejects when
// a disk device other than the root disk has a path that overlaps proofDir:
// equal to it, an ancestor of it or below it, since a shared mount there
// could let another instance forge the proof (a mount below it can hold a
// proof file itself; gate L12 rejects it too, a rule simpler to audit than
// the reasons why it would be harmless). A disk path that is not absolute
// fails closed. Disks without a path and other devices with a path are
// returned as information. It is a best effort, documented as such.
func CheckIsolation(devices map[string]map[string]string, proofDir string) Isolation {
	proofDir = path.Clean(proofDir)
	out := Isolation{Causes: []DeviceFinding{}, Info: []DeviceFinding{}}
	keys := make([]string, 0, len(devices))
	for k := range devices {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, name := range keys {
		d := devices[name]
		p, hasPath := d["path"]
		f := DeviceFinding{Device: name, Type: d["type"], Path: p, Source: DiskSource(d)}
		if d["type"] != "disk" {
			if hasPath {
				f.Relation = OtherType
				out.Info = append(out.Info, f)
			}
			continue
		}
		switch {
		case !hasPath || p == "":
			f.Relation = NoPath
			out.Info = append(out.Info, f)
			continue
		case !strings.HasPrefix(p, "/"):
			f.Relation = NotAbsolute
			out.Causes = append(out.Causes, f)
			continue
		}
		clean := path.Clean(p)
		switch {
		case clean == "/":
			// the root disk
		case clean == proofDir:
			f.Relation = Equal
			out.Causes = append(out.Causes, f)
		case strings.HasPrefix(proofDir, clean+"/"):
			f.Relation = Ancestor
			out.Causes = append(out.Causes, f)
		case strings.HasPrefix(clean, proofDir+"/"):
			f.Relation = Below
			out.Causes = append(out.Causes, f)
		}
	}
	out.Reject = len(out.Causes) > 0
	return out
}

// DiskSource names what a non-root disk device mounts, so that devices of
// different instances can be compared: "host:<path>" for a host path,
// "volume:<pool>/<volume>" for a storage volume; empty for the root disk
// and other devices.
func DiskSource(d map[string]string) string {
	if d["type"] != "disk" || d["source"] == "" {
		return ""
	}
	if pool := d["pool"]; pool != "" {
		return "volume:" + pool + "/" + d["source"]
	}
	return "host:" + d["source"]
}
