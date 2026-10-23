package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"xmustard/api-go/internal/shims"
)

func runShims(args []string) {
	fs := flag.NewFlagSet("xmustard-ops shims", flag.ExitOnError)
	reap := fs.Bool("reap", false, "SIGKILL orphaned/runaway stdio shims; Darwin rechecks identity but has a residual PID-reuse race")
	if err := fs.Parse(args); err != nil {
		fatal(err.Error())
	}
	if fs.NArg() != 0 {
		fatalUsage("usage: xmustard-ops shims [--reap]")
	}
	processes, err := shims.ListForOperator()
	if err != nil {
		fatal(err.Error())
	}
	if *reap {
		err = shims.ReapCurrent(processes)
		if err != nil {
			fatal(err.Error())
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Summary   shims.Summary   `json:"shims"`
		Processes []shims.Process `json:"processes"`
	}{shims.Count(processes), processes}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
