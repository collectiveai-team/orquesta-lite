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
	"github.com/lionelchamorro/orquestalite/internal/web"
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
		serve := fs.Bool("serve", false, "also host the web dashboard while running")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address (with --serve)")
		_ = fs.Parse(args)
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		startDashboard(runCtx, *serve, *addr)
		teamPath := "team.json"
		exit(commands.Run(runCtx, commands.RunOptions{
			ProjectDir: ".",
			TeamPath:   teamPath,
			LogFormat:  eventlog.Format(*logFormat),
		}))

	case "factory":
		fs := flag.NewFlagSet("factory", flag.ExitOnError)
		force := fs.Bool("force", false, "replace an existing unfinished queue")
		statusOnly := fs.Bool("status", false, "print the factory queue and exit")
		createPR := fs.Bool("pr", false, "push each finished feature branch and open a PR via gh")
		logFormat := fs.String("log-format", "auto", "stdout log format: auto|verbose|human")
		serve := fs.Bool("serve", false, "also host the web dashboard while running")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address (with --serve)")
		_ = fs.Parse(args)
		featuresPath := ""
		if fs.NArg() > 0 {
			featuresPath = fs.Arg(0)
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		startDashboard(runCtx, *serve && !*statusOnly, *addr)
		exit(commands.Factory(runCtx, commands.FactoryOptions{
			ProjectDir:   ".",
			FeaturesPath: featuresPath,
			Force:        *force,
			StatusOnly:   *statusOnly,
			CreatePR:     *createPR,
			LogFormat:    eventlog.Format(*logFormat),
			Out:          os.Stdout,
		}))

	case "cost":
		exit(commands.Cost(ctx, ".", os.Stdout))

	case "doctor":
		exit(commands.Doctor(".", os.Stdout))

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

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "127.0.0.1:4173", "listen address for the dashboard")
		_ = fs.Parse(args)
		serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		fmt.Printf("orquestalite dashboard: http://%s\n", *addr)
		exit(web.Serve(serveCtx, *addr, "."))

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

// startDashboard hosts the web dashboard in the background while a run or
// factory command drives the loops in the foreground — the "one command"
// mode. It dies with the process; errors (e.g. port already in use) are
// reported but never abort the run.
func startDashboard(ctx context.Context, enabled bool, addr string) {
	if !enabled {
		return
	}
	fmt.Printf("orquestalite dashboard: http://%s\n", addr)
	go func() {
		if err := web.Serve(ctx, addr, "."); err != nil {
			fmt.Fprintln(os.Stderr, "dashboard:", err)
		}
	}()
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: orq-lite <command> [args]

Commands:
  init [--lang L] [dir] scaffold .orquestalite, team.json, prompts/ (--lang: python|node|go|auto)
  plan <plan.md>        invoke parser, write tasks.json (--append to add)
  run [--log-format F]  run review/task/fix loops over existing tasks.json (--log-format: auto|verbose|human)
  factory <features.md> develop each feature on its own branch (no args: resume; --status; --force; --serve; --pr)
  cost                  per-task spend rollup (run.log sessions priced via agtop)
  doctor                preflight the setup (git, team.json, CLIs, credentials) before spending
  status [--watch]      print tasks table (--watch refreshes until Ctrl+C)
  serve [--addr A]      web dashboard with live events (default 127.0.0.1:4173)
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
