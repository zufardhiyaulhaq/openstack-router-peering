// Command router-peering runs the router peering API, the reconciler, or the migrations.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = "usage: router-peering <api|reconciler|migrate> --config <path>"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/router-peering/router-peering.yaml", "config file")
	_ = fs.Parse(os.Args[2:])
	switch cmd {
	case "api", "reconciler", "migrate":
		fmt.Fprintf(os.Stderr, "%s: not implemented yet (config %s)\n", cmd, *cfgPath)
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}
