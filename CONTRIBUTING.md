# Contributing

This document defines how changes are proposed, written, and merged in this repository. It applies to every contributor.

## 1. Branching Model

| Branch | Purpose | Created from | Merges into |
|---|---|---|---|
| `main` | Always releasable. Never committed to directly after the initial commit. | n/a | n/a |
| `feature/<short-description>` | New capability, documentation, or refactoring | `main` | `main` via pull request |
| `bugfix/<short-description>` | Fix for a defect | `main` | `main` via pull request |

Naming rules:
- Use lowercase kebab case, for example `feature/nginx-ingress-collector` or `bugfix/cgroup-memory-limit-parse`.
- If an issue exists, put its number first: `feature/42-isometric-port-badges`.
- Keep branches short-lived. Rebase on `main` before opening or updating a pull request.

## 2. Commit Messages

Commits follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).

```text
<type>(<optional scope>): <subject>

<body>

<footer>
```

**Types**

| Type | Use for |
|---|---|
| `feat` | A new capability |
| `fix` | A defect fix |
| `docs` | Documentation only |
| `refactor` | Code change that neither fixes a defect nor adds a capability |
| `perf` | Performance improvement |
| `test` | Adding or correcting tests |
| `build` | Dependencies, packaging, build tooling |
| `ci` | Continuous-integration configuration |
| `chore` | Maintenance that does not change behaviour |
| `revert` | Reverting an earlier commit |

**Scopes** in this repository: `collect`, `bus`, `store`, `service`, `api`, `mcp`, `cli`, `web`, `topology`, `proto`, `deploy`, `docs`, `repo`.

**Subject line**
- Imperative mood ("add", not "added" or "adds").
- Lowercase, no trailing full stop, at most 72 characters.

**Body**
- Explains *what* changed and *why*, not how.
- Wrapped at 72 characters.
- Uses bullet points for lists of changes.

**Footer**
- References issues (`Refs: #42`, `Closes: #42`).
- Declares breaking changes (`BREAKING CHANGE: <description>`).

**Authorship**
- A commit is authored solely by the person who makes it.
- Commit messages contain no extraneous trailers.

Example:

```text
feat(collect): parse nginx upstreams for ingress topology edges

Nginx reverse proxies on single-host deployments often route public
domains to local ports or Docker containers. Parsing active server and
upstream blocks exposes load-balanced targets on the topology map.

- read listen, server_name, upstream, and proxy_pass directives
- map upstream targets to known workloads by IP and published port
- add table-driven fixture tests under internal/collect/ingress/testdata

Refs: #14
```

## 3. Pull Requests

- Each pull request covers one logical change and targets `main`.
- The title follows the commit-message format, for example `feat(topology): add isometric block view`. It becomes the squash-commit subject.
- The description uses `.github/pull_request_template.md`:
  - summary;
  - changes;
  - motivation and context;
  - testing performed with evidence;
  - risks and rollback.
- Descriptions are written in a formal, factual tone.
- Every pull request is reviewed before merge, merged with **squash and merge**, and the branch is deleted afterward.

## 4. What Is Never Committed

- Secrets and local configuration: `.env*`, keys, certificates, tokens, credentials, `asterism.local.toml`.
- Local runtime state and data: `*.db`, `*.sqlite*`, `data/`, `tmp/`.
- Build artifacts and dependencies: `bin/`, `web/dist/`, `web/node_modules/`.
- Generated code changes made by hand: update `proto/asterism/v1/*.proto` and regenerate via `make gen`.

## 5. Before Opening a Pull Request

```bash
make lint test          # linters, type checks, Go and web unit tests
git status --porcelain  # verify nothing unintended is staged
```
