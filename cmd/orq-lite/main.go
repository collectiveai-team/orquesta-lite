package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
	commands.Version = version

	switch cmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		lang := fs.String("lang", "", "language hint: python|node|go|auto (default: autodetect)")
		precommit := fs.Bool("precommit", false, "write .pre-commit-config and set team.json lint_command for the detected language")
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		exit(commands.InitWithOptions(dir, commands.InitOptions{Lang: *lang, Precommit: *precommit}))

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
		fast := fs.Bool("fast", false, "batch all pending tasks through coder once, then tester/critic once")
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
			FastMode:   *fast,
		}))

	case "factory":
		fs := flag.NewFlagSet("factory", flag.ExitOnError)
		force := fs.Bool("force", false, "replace an existing unfinished queue")
		statusOnly := fs.Bool("status", false, "print the factory queue and exit")
		createPR := fs.Bool("pr", false, "push each finished feature branch and open a PR via gh")
		resume := fs.Bool("resume", false, "retry failed features (reuses their persisted task lists)")
		replan := fs.Bool("replan", false, "force fresh task decomposition for every feature (discards tasks-F*.json)")
		logFormat := fs.String("log-format", "auto", "stdout log format: auto|verbose|human")
		fast := fs.Bool("fast", false, "run coder/tester/critic once per feature, then final global review")
		serve := fs.Bool("serve", true, "host the web dashboard while running (on by default)")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address")
		_ = fs.Parse(args)
		featuresPath := ""
		if fs.NArg() > 0 {
			featuresPath = fs.Arg(0)
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		// The dashboard is on by default for factory runs so the operator
		// always sees its URL and can tell the run is live; --serve=false and
		// --status both suppress it.
		startDashboard(runCtx, *serve && !*statusOnly, *addr)
		exit(commands.Factory(runCtx, commands.FactoryOptions{
			ProjectDir:   ".",
			FeaturesPath: featuresPath,
			Force:        *force,
			StatusOnly:   *statusOnly,
			CreatePR:     *createPR,
			Resume:       *resume,
			Replan:       *replan,
			LogFormat:    eventlog.Format(*logFormat),
			FastMode:     *fast,
			Out:          os.Stdout,
		}))

	case "cost":
		exit(commands.Cost(ctx, ".", os.Stdout))

	case "precommit":
		fs := flag.NewFlagSet("precommit", flag.ExitOnError)
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		exit(commands.InitPrecommit(dir))

	case "review":
		fs := flag.NewFlagSet("review", flag.ExitOnError)
		pr := fs.String("pr", "", "PR number to review (resolved to base/head via gh)")
		base := fs.String("base", "", "explicit base git ref (overrides --pr resolution)")
		head := fs.String("head", "", "explicit head git ref (overrides --pr resolution)")
		publish := fs.Bool("publish", false, "post the verdict as a PR review via gh")
		_ = fs.Parse(args)
		exit(commands.Review(ctx, commands.ReviewOptions{
			ProjectDir: ".",
			PR:         *pr,
			Base:       *base,
			Head:       *head,
			Publish:    *publish,
		}))

	case "intake":
		fs := flag.NewFlagSet("intake", flag.ExitOnError)
		issue := fs.String("issue", "", "path to a file holding the GitHub issue body")
		noRun := fs.Bool("no-run", false, "stop after writing tasks.json (do not run the loop)")
		_ = fs.Parse(args)
		exit(commands.Intake(ctx, commands.IntakeOptions{
			ProjectDir: ".",
			IssuePath:  *issue,
			Run:        !*noRun,
		}))

	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		interval := fs.Duration("interval", 60*time.Second, "poll interval")
		issues := fs.Bool("issues", false, "watch issues (trigger intake)")
		prs := fs.Bool("prs", false, "watch PRs (trigger review)")
		reviewOwn := fs.Bool("review-own-prs", false, "review PRs that orq-lite itself opened (default: skip)")
		publishPRs := fs.Bool("publish-prs", false, "post PR reviews via gh when reviewing")
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		watchCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		exit(commands.Watch(watchCtx, commands.WatchOptions{
			ProjectDir:   dir,
			Interval:     *interval,
			Issues:       *issues,
			PRs:          *prs,
			ReviewOwnPRs: *reviewOwn,
			PublishPRs:   *publishPRs,
			Out:          os.Stdout,
		}))

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

	case "flow":
		if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
			if names := commands.ListFlowNames("flows.json"); len(names) > 0 {
				fmt.Fprintln(os.Stderr, "Available flows in flows.json:")
				for _, n := range names {
					fmt.Fprintf(os.Stderr, "  %s\n", n)
				}
			}
			fmt.Fprintln(os.Stderr, `Usage: orq-lite flow run <name> [key=value ...] [--log-format F]
  Execute a configured flow from flows.json.`)
			os.Exit(0)
		}
		if args[0] != "run" {
			fmt.Fprintln(os.Stderr, "usage: orq-lite flow run <name> [key=value ...]")
			os.Exit(2)
		}
		// Separate flags from positionals by hand: the stdlib flag package stops
		// at the first positional, which would push a trailing `--log-format`
		// into the key=value inputs. Here flags may appear in any position.
		logFormat := "auto"
		var positional []string
		for i := 1; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "--log-format" || a == "-log-format":
				if i+1 < len(args) {
					logFormat = args[i+1]
					i++
				}
			case strings.HasPrefix(a, "--log-format="):
				logFormat = strings.TrimPrefix(a, "--log-format=")
			case strings.HasPrefix(a, "-log-format="):
				logFormat = strings.TrimPrefix(a, "-log-format=")
			default:
				positional = append(positional, a)
			}
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		if len(positional) == 0 {
			fmt.Fprintln(os.Stderr, "usage: orq-lite flow run <name> [key=value ...] [--log-format F]")
			os.Exit(2)
		}
		exit(commands.RunFlow(runCtx, commands.FlowOptions{
			ProjectDir: ".",
			TeamPath:   "team.json",
			FlowsPath:  "flows.json",
			FlowName:   positional[0],
			InputArgs:  positional[1:],
			LogFormat:  eventlog.Format(logFormat),
		}))

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
  init [--lang L] [dir] scaffold .orquestalite, team.json, prompts/ (--lang: python|node|go|auto; --precommit writes .pre-commit-config + lint_command)
  plan <plan.md>        invoke parser, write tasks.json (--append to add)
  run [--log-format F]  run review/task/fix loops over existing tasks.json (--log-format: auto|verbose|human; --fast batches pending tasks)
  factory <features.md> develop each feature on its own branch (--fast batches each feature; no args: resume queue; --resume: retry failed features; --replan: fresh decomposition; --status; --force; --serve; --pr)
  precommit [dir]       write .pre-commit-config + set team.json lint_command for the detected language
  review [--pr N|--base B --head H] [--publish] critic-review a PR diff and post the verdict via gh
  intake --issue <file> triage a GitHub issue: plan+run, or write missing_info (--no-run)
  watch <project> [--issues] [--prs] [--interval D] poll GitHub via gh; intake new issues, review new PRs (--review-own-prs, --publish-prs)
  cost                  per-task spend rollup (run.log sessions priced via agtop)
  doctor                preflight the setup (git, team.json, CLIs, credentials) before spending
  status [--watch]      print tasks table (--watch refreshes until Ctrl+C)
  serve [--addr A]      web dashboard with live events (default 127.0.0.1:4173)
  log [--role R]        replay .orquestalite/run.log (--event T, --expand N, --full)
  reset                 remove .orquestalite state
  update [--check]      download and install the latest release from GitHub
  flow run <name>       execute a flow from flows.json (e.g. orq-lite flow run factory features.md)
  version               print the binary version`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
