// Package server implements the commands of the server environment, which
// talk to the LXD REST API (spec: Server environment).
package server

// Commands is the command tree of the server environment.
type Commands struct {
	Info        Info        `command:"info" description:"Record the server's version, API extensions and TLS parameters (PRS-01)."`
	Whoami      Whoami      `command:"whoami" description:"Record the identity and permissions the server reports for the probe (PRS-02)."`
	Instance    Instance    `command:"instance" description:"Inspect instances."`
	Project     Project     `command:"project" description:"Inspect projects."`
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

// Project groups the project verbs.
type Project struct {
	Show ProjectShow `command:"show" description:"Record a project and its features.networks (PRS-13)."`
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
