package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/commands"
)

var version = "dev"

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
			fmt.Fprintln(os.Stderr, "usage: orq-lite plan <plan.md> [--append]")
			os.Exit(2)
		}
		exit(commands.PlanWithLiveCaller(ctx, ".", fs.Arg(0), *appendFlag))

	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		_ = fs.Parse(args)
		teamPath := "team.json"
		exit(commands.Run(ctx, commands.RunOptions{ProjectDir: ".", TeamPath: teamPath}))

	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		watch := fs.Bool("watch", false, "refresh status every interval until Ctrl+C")
		interval := fs.Duration("interval", time.Second, "watch refresh interval")
		_ = fs.Parse(args)
		if *watch {
			watchCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()
			exit(commands.StatusWatch(watchCtx, ".", os.Stdout, *interval))
		} else {
			exit(commands.Status(".", os.Stdout))
		}

	case "reset":
		exit(commands.Reset("."))

	case "update", "upgrade":
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		check := fs.Bool("check", false, "report whether an update is available without installing")
		_ = fs.Parse(args)
		exit(commands.Update(ctx, commands.UpdateOptions{
			CurrentVersion: version,
			CheckOnly:      *check,
			Out:            os.Stdout,
		}))

	case "version", "--version", "-v":
		fmt.Println(version)

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: orq-lite <command> [args]

Commands:
  init [dir]            scaffold .orquestalite, team.json, prompts/
  plan <plan.md>        invoke parser, write tasks.json (--append to add)
  run                   run review/task/fix loops over existing tasks.json
  status [--watch]      print tasks table (--watch refreshes until Ctrl+C)
  reset                 remove .orquestalite state
  update [--check]      download and install the latest release from GitHub
  version               print the binary version`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
