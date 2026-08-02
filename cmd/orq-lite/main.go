package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/commands"
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
		appendFlag := fs.Bool("append", false, "append tickets to the current workflow plan")
		_ = fs.Parse(args)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: orq-lite plan <plan.md> [--append]")
			os.Exit(2)
		}
		exit(commands.RunDevelopmentAlias(ctx, ".", "plan", map[string]any{"plan_path": fs.Arg(0), "append": *appendFlag}, os.Stdout))

	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		fast := fs.Bool("fast", false, "use the pack's fast task-list path")
		serve := fs.Bool("serve", false, "also host the web dashboard while running")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address (with --serve)")
		_ = fs.Parse(args)
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		startDashboard(runCtx, *serve, *addr)
		exit(commands.RunDevelopmentAlias(runCtx, ".", "run", map[string]any{"fast": *fast}, os.Stdout))

	case "factory":
		fs := flag.NewFlagSet("factory", flag.ExitOnError)
		createPR := fs.Bool("pr", false, "push each finished feature branch and open a PR via gh")
		fast := fs.Bool("fast", false, "use the pack's fast implementation path")
		serve := fs.Bool("serve", true, "host the web dashboard while running (on by default)")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address")
		_ = fs.Parse(args)
		featuresPath := ""
		if fs.NArg() > 0 {
			featuresPath = fs.Arg(0)
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		startDashboard(runCtx, *serve, *addr)
		exit(commands.RunDevelopmentAlias(runCtx, ".", "factory", map[string]any{"features_path": featuresPath, "fast": *fast, "create_pr": *createPR}, os.Stdout))

	case "cost":
		exit(commands.Cost(ctx, ".", os.Stdout))

	case "review":
		fs := flag.NewFlagSet("review", flag.ExitOnError)
		pr := fs.String("pr", "", "PR number to review (resolved to base/head via gh)")
		base := fs.String("base", "", "explicit base git ref (overrides --pr resolution)")
		head := fs.String("head", "", "explicit head git ref (overrides --pr resolution)")
		publish := fs.Bool("publish", false, "post the verdict as a PR review via gh")
		_ = fs.Parse(args)
		exit(commands.RunDevelopmentAlias(ctx, ".", "review", map[string]any{"pr": *pr, "base": *base, "head": *head, "publish": *publish}, os.Stdout))

	case "intake":
		fs := flag.NewFlagSet("intake", flag.ExitOnError)
		issue := fs.String("issue", "", "path to a file holding the GitHub issue body")
		noRun := fs.Bool("no-run", false, "stop after intake planning")
		_ = fs.Parse(args)
		exit(commands.RunDevelopmentAlias(ctx, ".", "intake", map[string]any{"issue_path": *issue, "run": !*noRun}, os.Stdout))

	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		interval := fs.Duration("interval", 60*time.Second, "poll interval")
		issues := fs.Bool("issues", false, "watch issues (trigger intake)")
		prs := fs.Bool("prs", false, "watch PRs (trigger review)")
		reviewOwn := fs.Bool("review-own-prs", false, "review PRs that orq-lite itself opened (default: skip)")
		publishPRs := fs.Bool("publish-prs", false, "post PR reviews via gh when reviewing")
		issueFlow := fs.String("issue-flow", "development/issue-fix@1", "flow for issues")
		prFlow := fs.String("pr-flow", "development/pr-review@1", "flow for pull requests")
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
			IssueFlow:    *issueFlow,
			PRFlow:       *prFlow,
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

	case "pack":
		exit(commands.PackCLI(ctx, ".", args, os.Stdout))

	case "flow":
		if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(os.Stderr, `Usage: orq-lite flow <command>
  validate <ref|path>        compile and validate a v2 flow
  inspect <ref|path>         print pinned compiled IR
	  list                       list local and installed v2 flows
	  run <ref|path> [k=v...]    start a durable workflow
  status <run-id>            print run, steps and approvals
  resume <run-id>            resume the pinned workflow definition
  cancel <run-id>            cancel a workflow
  approve <run> <id> --decision approve|reject
  events <run-id>            print durable workflow events`)
			os.Exit(0)
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		exit(commands.FlowCLI(runCtx, ".", args, os.Stdout))

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
  init [--lang L] [dir] scaffold team.json and the built-in development pack
  plan <plan.md>        run development/plan-tickets@1
  run [--fast]          run development/task-list@1
  factory <features.md> run development/factory-governed@1
  review [--pr N|--base B --head H] [--publish] critic-review a PR diff and post the verdict via gh
  intake --issue <file> triage a GitHub issue: plan+run, or write missing_info (--no-run)
  watch <project> [--issues] [--prs] [--interval D] poll GitHub; v2 emits idempotent generic flow triggers (--issue-flow, --pr-flow)
  cost                  per-task spend rollup (run.log sessions priced via agtop)
  doctor                preflight packs, team.json, gates, CLIs and credentials before spending
  status [--watch]      print durable workflow runs (--watch refreshes until Ctrl+C)
  serve [--addr A]      web dashboard with live events (default 127.0.0.1:4173)
  log [--role R]        replay .orquestalite/run.log (--event T, --expand N, --full)
  reset                 remove .orquestalite state
  update [--check]      download and install the latest release from GitHub
  pack install <dir>    verify a v2 pack and install it into .orquestalite/packs/
  pack list             list installed packs (name, version, digest, file count)
  flow validate|inspect <ref|path> compile a strict v2 flow without executing it
  flow list             list local and installed versioned v2 flows
  flow run <ref|path>   execute v2 flow data (--policy=<ref|path>, --source-key=<stable-key>, key=value...)
  flow status|events <run-id> inspect durable workflow state/history
  flow resume|cancel <run-id> resume the pinned IR or cancel a run
  flow approve <run-id> <approval-id> --decision approve|reject
  version               print the binary version`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
