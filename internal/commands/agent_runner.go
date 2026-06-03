package commands

import (
	"context"

	"github.com/lionelchamorro/orquestalite/internal/runner"
)

type AgentRunner interface {
	Run(ctx context.Context, s runner.Spec) (*runner.Result, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, s runner.Spec) (*runner.Result, error) {
	return runner.RunAgent(ctx, s)
}
