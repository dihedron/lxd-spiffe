// Package server implements the commands of the server environment, which
// talk to the LXD REST API (spec: Server environment).
package server

import (
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// Commands is the command tree of the server environment.
type Commands struct {
	Info        Info        `command:"info" description:"Record the server's version, API extensions and TLS parameters (PRS-01)."`
	Whoami      Whoami      `command:"whoami" description:"Record the identity and permissions the server reports for the probe (PRS-02)."`
	Instance    Instance    `command:"instance" description:"Inspect instances."`
	Config      Config      `command:"config" description:"Read and write lab config keys of an instance."`
	File        File        `command:"file" description:"Use the files API of an instance."`
	Permissions Permissions `command:"permissions" description:"Run the read-only permission matrix for the presented identity (PRS-40)."`
	Bearer      Bearer      `command:"bearer" description:"Check bearer token authentication."`
	Rehearse    Rehearse    `command:"rehearse" description:"Rehearse the attestor's proofs against a lab instance."`
}

// Instance groups the instance verbs.
type Instance struct {
	Show  InstanceShow  `command:"show" description:"Record an instance, its ETag and volatile keys (PRS-10)."`
	Watch InstanceWatch `command:"watch" description:"Poll an instance and record changes of its identity keys (PRS-11)."`
	List  InstanceList  `command:"list" description:"Record the instances of the project (PRS-12)."`
}

// Config groups the config verbs.
type Config struct {
	Get   ConfigGet   `command:"get" description:"Read a config key (PRS-20)."`
	Set   ConfigSet   `command:"set" description:"Write a config key with PATCH or PUT (PRS-21)."`
	Unset ConfigUnset `command:"unset" description:"Try a way of removing a config key (PRS-22)."`
	Race  ConfigRace  `command:"race" description:"Run concurrent writers on a config key (PRS-23)."`
}

// File groups the files API verbs.
type File struct {
	Stat FileStat `command:"stat" description:"Record every header the files API returns for a path (PRS-30)."`
	Get  FileGet  `command:"get" description:"Read a file through the files API (PRS-31)."`
	Head FileHead `command:"head" description:"Try HEAD on the files API (PRS-32)."`
	Dir  FileDir  `command:"dir" description:"List a directory through the files API (PRS-33)."`
}

// Bearer groups the bearer verbs.
type Bearer struct {
	Check BearerCheck `command:"check" description:"Check a bearer token against the remote API (PRS-41)."`
}

// Rehearse groups the rehearsals.
type Rehearse struct {
	FilePull   RehearseFilePull   `command:"file-pull" description:"Rehearse the server side of file_pull (PRS-50)."`
	ConfigPush RehearseConfigPush `command:"config-push" description:"Rehearse the server side of config_push (PRS-51)."`
}

// instanceArgs is the positional argument of the verbs on one instance.
type instanceArgs struct {
	Name string `positional-arg-name:"NAME" description:"Instance name."`
}

// keyArgs are the positional arguments of the verbs on one config key.
type keyArgs struct {
	Name string `positional-arg-name:"NAME" description:"Instance name."`
	Key  string `positional-arg-name:"KEY" description:"Config key, under user.lxd-probe.*."`
}

// pathArgs are the positional arguments of the files API verbs.
type pathArgs struct {
	Name string `positional-arg-name:"NAME" description:"Instance name."`
	Path string `positional-arg-name:"PATH" description:"Path inside the instance."`
}

// Info is server info.
type Info struct {
	probe.ServerCommand
	ProbeTLS12 bool `long:"probe-tls12" description:"Also try a TLS 1.2 handshake and record whether the server accepts it (PRS-03)."`
}

// Execute runs the command.
func (cmd *Info) Execute(args []string) error {
	return probe.NotImplemented("server info")
}

// Whoami is server whoami.
type Whoami struct {
	probe.ServerCommand
}

// Execute runs the command.
func (cmd *Whoami) Execute(args []string) error {
	return probe.NotImplemented("server whoami")
}

// InstanceShow is server instance show NAME.
type InstanceShow struct {
	probe.ServerCommand
	Args instanceArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *InstanceShow) Execute(args []string) error {
	return probe.NotImplemented("server instance show")
}

// InstanceWatch is server instance watch NAME.
type InstanceWatch struct {
	probe.ServerCommand
	For      time.Duration `long:"for" description:"How long to watch." default:"10m"`
	Interval time.Duration `long:"interval" description:"Poll interval." default:"1s"`
	Args     instanceArgs  `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *InstanceWatch) Execute(args []string) error {
	return probe.NotImplemented("server instance watch")
}

// InstanceList is server instance list.
type InstanceList struct {
	probe.ServerCommand
}

// Execute runs the command.
func (cmd *InstanceList) Execute(args []string) error {
	return probe.NotImplemented("server instance list")
}

// ConfigGet is server config get NAME KEY.
type ConfigGet struct {
	probe.ServerCommand
	Args keyArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *ConfigGet) Execute(args []string) error {
	return probe.NotImplemented("server config get")
}

// ConfigSet is server config set NAME KEY VALUE.
type ConfigSet struct {
	probe.ServerCommand
	Method             string `long:"method" description:"PATCH with only the key, or PUT of the whole instance." choice:"patch" choice:"put" default:"patch"`
	IfMatch            string `long:"if-match" description:"ETag to send: the one read just before, none, or a stale one." choice:"auto" choice:"none" choice:"stale" default:"auto"`
	Wait               bool   `long:"wait" description:"Wait for the operation to complete."`
	ExpectInstanceUUID string `long:"expect-instance-uuid" description:"Refuse to write unless the instance's volatile.uuid is this."`
	Args               struct {
		Name  string `positional-arg-name:"NAME" description:"Instance name."`
		Key   string `positional-arg-name:"KEY" description:"Config key, under user.lxd-probe.*."`
		Value string `positional-arg-name:"VALUE" description:"Value to write."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *ConfigSet) Execute(args []string) error {
	return probe.NotImplemented("server config set")
}

// ConfigUnset is server config unset NAME KEY.
type ConfigUnset struct {
	probe.ServerCommand
	Method             string  `long:"method" description:"Way of removing the key." choice:"patch-empty" choice:"patch-null" choice:"put" required:"yes"`
	ExpectInstanceUUID string  `long:"expect-instance-uuid" description:"Refuse to write unless the instance's volatile.uuid is this."`
	Args               keyArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *ConfigUnset) Execute(args []string) error {
	return probe.NotImplemented("server config unset")
}

// ConfigRace is server config race NAME KEY.
type ConfigRace struct {
	probe.ServerCommand
	Writers            int     `long:"writers" description:"Number of concurrent writers." default:"8"`
	Retries            int     `long:"retries" description:"Retry budget per writer on 412." default:"3"`
	ExpectInstanceUUID string  `long:"expect-instance-uuid" description:"Refuse to write unless the instance's volatile.uuid is this."`
	Args               keyArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *ConfigRace) Execute(args []string) error {
	return probe.NotImplemented("server config race")
}

// FileStat is server file stat NAME PATH.
type FileStat struct {
	probe.ServerCommand
	Args pathArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FileStat) Execute(args []string) error {
	return probe.NotImplemented("server file stat")
}

// FileGet is server file get NAME PATH.
type FileGet struct {
	probe.ServerCommand
	MaxBytes int64    `long:"max-bytes" description:"Read limit, in bytes." default:"1048576"`
	Args     pathArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FileGet) Execute(args []string) error {
	return probe.NotImplemented("server file get")
}

// FileHead is server file head NAME PATH.
type FileHead struct {
	probe.ServerCommand
	Args pathArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FileHead) Execute(args []string) error {
	return probe.NotImplemented("server file head")
}

// FileDir is server file dir NAME PATH.
type FileDir struct {
	probe.ServerCommand
	Args pathArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FileDir) Execute(args []string) error {
	return probe.NotImplemented("server file dir")
}

// Permissions is server permissions NAME.
type Permissions struct {
	probe.ServerCommand
	Args instanceArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *Permissions) Execute(args []string) error {
	return probe.NotImplemented("server permissions")
}

// BearerCheck is server bearer check.
type BearerCheck struct {
	probe.ServerCommand
	WaitExpiry bool          `long:"wait-expiry" description:"Keep calling until the token expires and record the first failure."`
	MaxWait    time.Duration `long:"max-wait" description:"Upper bound for --wait-expiry." default:"1h"`
}

// Execute runs the command.
func (cmd *BearerCheck) Execute(args []string) error {
	return probe.NotImplemented("server bearer check")
}

// RehearseFilePull is server rehearse file-pull NAME.
type RehearseFilePull struct {
	probe.ServerCommand
	Args instanceArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *RehearseFilePull) Execute(args []string) error {
	return probe.NotImplemented("server rehearse file-pull")
}

// RehearseConfigPush is server rehearse config-push NAME.
type RehearseConfigPush struct {
	probe.ServerCommand
	Args instanceArgs `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *RehearseConfigPush) Execute(args []string) error {
	return probe.NotImplemented("server rehearse config-push")
}
