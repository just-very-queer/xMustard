package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

func runTrust(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: xmustard-ops trust <grant|revoke|list> [path] [--data-dir DIR]")
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("trust "+action, flag.ContinueOnError)
	dataDir := flags.String("data-dir", envDefault("XMUSTARD_DATA_DIR", "../backend/data"), "operator data directory")
	path := flags.String("path", "", "repository root")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *path == "" && flags.NArg() == 1 {
		*path = flags.Arg(0)
	}
	if flags.NArg() > 1 || (action != "list" && strings.TrimSpace(*path) == "") || (action == "list" && strings.TrimSpace(*path) != "") {
		fmt.Fprintln(os.Stderr, "usage: xmustard-ops trust <grant|revoke|list> [path] [--data-dir DIR]")
		return 2
	}
	switch action {
	case "grant", "revoke":
		roots, err := workspaceops.UpdateRepoTrust(*dataDir, *path, action == "grant")
		if err != nil {
			fmt.Fprintln(os.Stderr, "trust:", err)
			return 1
		}
		for _, root := range roots {
			fmt.Fprintln(os.Stdout, root)
		}
		return 0
	case "list":
		roots, err := workspaceops.ListRepoTrust(*dataDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "trust:", err)
			return 1
		}
		for _, root := range roots {
			fmt.Fprintln(os.Stdout, root)
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: xmustard-ops trust <grant|revoke|list> [path] [--data-dir DIR]")
		return 2
	}
}
