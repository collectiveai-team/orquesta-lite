package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

type ArtifactExecutor struct {
	Root             string
	MaxBytes         int64
	AllowedReadRoots []string
}

func (a *ArtifactExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "artifact.capture", Version: "1", Effect: activity.EffectIdempotent}
}

type artifactInput struct {
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Text        *string `json:"text,omitempty"`
	BytesBase64 string  `json:"bytesBase64,omitempty"`
	File        string  `json:"file,omitempty"`
}

var safeArtifact = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (a *ArtifactExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input artifactInput
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("artifact.capture input", err)
	}
	if a.Root == "" || input.Name == "" {
		return activity.Result{}, contractError("artifact.capture input", fmt.Errorf("root and name are required"))
	}
	sources := 0
	if input.Text != nil {
		sources++
	}
	if input.BytesBase64 != "" {
		sources++
	}
	if input.File != "" {
		sources++
	}
	if sources != 1 {
		return activity.Result{}, contractError("artifact.capture input", fmt.Errorf("set exactly one of text, bytesBase64, or file"))
	}
	var content []byte
	var err error
	switch {
	case input.Text != nil:
		content = []byte(*input.Text)
	case input.BytesBase64 != "":
		content, err = base64.StdEncoding.DecodeString(input.BytesBase64)
		if err != nil {
			return activity.Result{}, contractError("artifact.capture input", fmt.Errorf("bytesBase64: %w", err))
		}
	case input.File != "":
		path, allowed := allowedArtifactFile(input.File, a.AllowedReadRoots)
		if !allowed {
			return activity.Result{}, &activity.Error{Class: activity.ErrorPermissionDenied, Op: "artifact.capture", Err: fmt.Errorf("file %s is outside allowed roots", input.File)}
		}
		content, err = os.ReadFile(path)
		if err != nil {
			return activity.Result{}, err
		}
	}
	if a.MaxBytes > 0 && int64(len(content)) > a.MaxBytes {
		return activity.Result{}, &activity.Error{Class: activity.ErrorPermissionDenied, Op: "artifact.capture", Err: fmt.Errorf("artifact exceeds %d bytes", a.MaxBytes)}
	}
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	name := safeArtifact.ReplaceAllString(input.Name, "-")
	dir := filepath.Join(a.Root, "workflow", request.ScopePath, request.StepID)
	if request.ForeachKey != "" {
		dir = filepath.Join(dir, safeArtifact.ReplaceAllString(request.ForeachKey, "-"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return activity.Result{}, err
	}
	path := filepath.Join(dir, name)
	if existing, err := os.ReadFile(path); err == nil && !bytes.Equal(existing, content) {
		return activity.Result{}, &activity.Error{Class: activity.ErrorConflict, Op: "artifact.capture", Err: fmt.Errorf("artifact %s already exists with different content", path)}
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return activity.Result{}, err
	}
	output, _ := json.Marshal(map[string]string{"uri": filepath.ToSlash(path), "digest": digest})
	return activity.Result{Output: output, Artifacts: []activity.Artifact{{Kind: input.Kind, URI: filepath.ToSlash(path), Digest: digest}}}, nil
}

func allowedArtifactFile(path string, roots []string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		rootAbsolute, rootErr := filepath.Abs(root)
		if rootErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(rootAbsolute, absolute)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return absolute, true
		}
	}
	return "", false
}

type ApprovalExecutor struct{}

func (ApprovalExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "human.wait_for_approval", Version: "1", Effect: activity.EffectPure}
}
func (ApprovalExecutor) Execute(_ context.Context, request activity.Request) (activity.Result, error) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := strictJSON(request.Inputs, &input); err != nil {
		return activity.Result{}, contractError("human.wait_for_approval input", err)
	}
	if input.Reason == "" {
		return activity.Result{}, contractError("human.wait_for_approval input", fmt.Errorf("reason is required"))
	}
	return activity.Result{}, &activity.ApprovalRequiredError{Reason: input.Reason, Input: append([]byte(nil), request.Inputs...)}
}
