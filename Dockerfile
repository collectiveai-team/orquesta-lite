# orquestalite factory image: orq-lite + the three agent CLIs (claude, codex,
# gemini). Credentials are NOT baked in — mount them at runtime (see
# docker-compose.yml / docs/docker.md).

# --- build stage -------------------------------------------------------------
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/orq-lite ./cmd/orq-lite

# --- runtime stage -----------------------------------------------------------
FROM node:22-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl ripgrep procps \
 && rm -rf /var/lib/apt/lists/*

# Agent CLIs. Pin via build args so image rebuilds are reproducible;
# override with --build-arg to upgrade.
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_VERSION=latest
ARG GEMINI_CLI_VERSION=latest
RUN npm install -g --omit=dev \
      @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
      @openai/codex@${CODEX_VERSION} \
      @google/gemini-cli@${GEMINI_CLI_VERSION} \
 && npm cache clean --force

# Optional: headless chromium for browser-real verification of web apps
# (the verifier role drives it via playwright). Adds ~400MB; enable with
#   docker compose build --build-arg INSTALL_PLAYWRIGHT=1
ARG INSTALL_PLAYWRIGHT=0
RUN if [ "$INSTALL_PLAYWRIGHT" = "1" ]; then \
      npm install -g playwright \
      && playwright install --with-deps chromium \
      && npm cache clean --force; \
    fi

COPY --from=build /out/orq-lite /usr/local/bin/orq-lite

# Non-root: reuse the node base image's uid-1000 user (matches the common
# host uid, so mounted credential dirs stay writable — the CLIs refresh OAuth
# tokens in place). Agent CLIs read credentials from $HOME: ~/.claude,
# ~/.codex, ~/.gemini.
USER node
ENV HOME=/home/node
WORKDIR /workspace

# git identity for the per-task commits the orchestrator makes; override via env.
ENV GIT_AUTHOR_NAME=orquestalite \
    GIT_AUTHOR_EMAIL=orquestalite@local \
    GIT_COMMITTER_NAME=orquestalite \
    GIT_COMMITTER_EMAIL=orquestalite@local

EXPOSE 4173
ENTRYPOINT ["orq-lite"]
CMD ["--help"]
