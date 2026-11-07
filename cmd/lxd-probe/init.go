package main

import (
	"os"
	"path"

	"github.com/dihedron/lxd-spiffe/internal/startup"
)

func init() {
	startup.Init(path.Base(os.Args[0]))
}
