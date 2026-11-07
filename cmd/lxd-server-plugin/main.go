package main

import (
	"os"

	"github.com/dihedron/lxd-spiffe/cmd/lxd-server-plugin/command"
	"github.com/dihedron/lxd-spiffe/internal/plugin/server/lxdiid"
	"github.com/jessevdk/go-flags"
	"github.com/spiffe/spire-plugin-sdk/pluginmain"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
)

func main() {
	os.Exit(run())
}

// run serves the plugin or parses the command line; it returns the exit code
// so that the deferred cleanup runs before the process exits.
func run() int {
	defer cleanup()

	// SPIRE Server starts the plugin without arguments
	if len(os.Args) == 1 {
		plugin := lxdiid.New()
		pluginmain.Serve(
			nodeattestorv1.NodeAttestorPluginServer(plugin),
			configv1.ConfigServiceServer(plugin),
		)
		return 0
	}

	options := command.Commands{}
	if _, err := flags.NewParser(&options, flags.Default).Parse(); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			return 0
		}
		return 1
	}
	return 0
}
