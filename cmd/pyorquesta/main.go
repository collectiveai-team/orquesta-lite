package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/lionelchamorro/pyorquesta/internal/commands"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	ctx := context.Background()

	switch cmd {
	case "init":
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		exit(commands.Init(dir))

	case "plan":
		fs := flag.NewFlagSet("plan", flag.ExitOnError)
		appendFlag := fs.Bool("append", false, "append to existing tasks.json")
		_ = fs.Parse(args)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: pyorquesta plan <plan.md> [--append]")
			os.Exit(2)
		}
		exit(commands.PlanWithLiveCaller(ctx, ".", fs.Arg(0), *appendFlag))

	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		_ = fs.Parse(args)
		teamPath := "team.json"
		exit(commands.Run(ctx, commands.RunOptions{ProjectDir: ".", TeamPath: teamPath}))

	case "status":
		exit(commands.Status(".", os.Stdout))

	case "reset":
		exit(commands.Reset("."))

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: pyorquesta <command> [args]

Commands:
  init [dir]            scaffold .pyorquesta, team.json, prompts/
  plan <plan.md>        invoke parser, write tasks.json (--append to add)
  run                   run review/task/fix loops over existing tasks.json
  status                print tasks table
  reset                 remove .pyorquesta state`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
