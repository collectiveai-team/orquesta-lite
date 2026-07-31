package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// loopGateFlow mirrors the governed pack's shape without needing agents: a
// while loop, followed by a gate.assert on the value the loop ended with,
// reached through `.last`. Each pass carries the previous pass's command.run
// output forward, so the condition and the gate read the same shape.
const loopGateFlow = `{
 "apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"looped","version":"1"},
 "inputs":{"budget":{"schema":"schema:any@1"}},
 "steps":[
  {"id":"advance","uses":"activity:command.run@1",
   "while":{"condition":"item.exitCode == 0","maxIterations":3,
            "initial":{"exitCode":0}},
   "with":{"argv":["/usr/bin/true"]}},
  {"id":"plan_complete","uses":"activity:gate.assert@1",
   "with":{"value":{"$ref":"steps.advance.output.last.exitCode"},"equals":0,
           "message":"the loop's last pass did not succeed"}}
 ]}`

const anySchema = `{}`

func installLoopGatePack(t *testing.T, dir string) {
	t.Helper()
	installFixturePack(t, dir, "loops", "1", map[string]string{
		"flows/looped@1.json": loopGateFlow,
		"schemas/any@1.json":  anySchema,
	})
}

// gate.assert must be registered in the live runtime registry, not merely
// declared in builtinSpecs() — a spec the compiler knows about but the runtime
// cannot dispatch fails only once the step is reached.
func TestGateAssertIsDispatchableInARealRun(t *testing.T) {
	dir := t.TempDir()
	installLoopGatePack(t, dir)
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "loops/looped@1", "budget=3"}, &out); err != nil {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "status=succeeded") {
		t.Fatalf("out=%s", out.String())
	}
}

// The gate reads the loop's final pass through `.last`. If the loop's last
// value fails the assertion, the run must fail — this is the shape that
// replaced a shell-out to a JSON parser, and it has to stay blocking.
func TestGateAssertOnLoopResultBlocksTheRun(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "loops", "1", map[string]string{
		"schemas/any@1.json": anySchema,
		// Same shape, but the gate demands an exit code the command never returns.
		"flows/looped@1.json": strings.Replace(loopGateFlow, `"equals":0`, `"equals":42`, 1),
	})
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "loops/looped@1", "budget=2"}, &out)
	if err == nil {
		t.Fatalf("a failed assertion must fail the run: out=%s", err)
	}
	if !strings.Contains(err.Error(), "the loop's last pass did not succeed") {
		t.Fatalf("the gate's message must reach the operator: %v", err)
	}
}
