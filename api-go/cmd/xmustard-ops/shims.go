package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"syscall"

	"xmustard/api-go/internal/shims"
)

func runShims(args []string) {
	fs := flag.NewFlagSet("xmustard-ops shims", flag.ExitOnError)
	reap := fs.Bool("reap", false, "kill orphaned and runaway stdio shims")
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
		err = shims.Reap(processes, func(pid int, signal os.Signal) error { return syscall.Kill(pid, signal.(syscall.Signal)) })
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
