package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/commands"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
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
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		lang := fs.String("lang", "", "language hint: python|node|go|auto (default: autodetect)")
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		exit(commands.InitWithOptions(dir, commands.InitOptions{Lang: *lang}))

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
		logFormat := fs.String("log-format", "auto", "stdout log format: auto|verbose|human")
		_ = fs.Parse(args)
		teamPath := "team.json"
		exit(commands.Run(ctx, commands.RunOptions{
			ProjectDir: ".",
			TeamPath:   teamPath,
			LogFormat:  eventlog.Format(*logFormat),
		}))

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

	case "log":
		fs := flag.NewFlagSet("log", flag.ExitOnError)
		role := fs.String("role", "", "show only agent_run events for this role")
		event := fs.String("event", "", "show only events of this type")
		expand := fs.Int("expand", 0, "print event #N in full (use the leading #N from the listing)")
		full := fs.Bool("full", false, "print full stdout/stderr/final_text tails for each agent_run")
		_ = fs.Parse(args)
		exit(commands.Log(".", os.Stdout, commands.LogViewOptions{
			Role:   *role,
			Event:  *event,
			Expand: *expand,
			Full:   *full,
		}))

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
  init [--lang L] [dir] scaffold .orquestalite, team.json, prompts/ (--lang: python|node|go|auto)
  plan <plan.md>        invoke parser, write tasks.json (--append to add)
  run [--log-format F]  run review/task/fix loops over existing tasks.json (--log-format: auto|verbose|human)
  status [--watch]      print tasks table (--watch refreshes until Ctrl+C)
  log [--role R]        replay .orquestalite/run.log (--event T, --expand N, --full)
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
