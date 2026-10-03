# CLAUDE.md

Guidance for Claude Code when working in this repository (Go, hosted on GitHub).

## Commands
- Build: `go build ./...`
- Test all: `go test -race ./...`
- Test one package: `go test ./internal/foo -run TestName -v`
- Vet: `go vet ./...`
- Lint: `golangci-lint run`
- Format: `gofmt -w .` (or `goimports -w .`)
- After changing dependencies: `go mod tidy`

## Definition of done
Before reporting a task as finished, run `gofmt`, `go vet ./...`, `golangci-lint run` and `go test -race ./...`, and fix anything they report. Say so explicitly if any step was skipped or failed.

## Layout
- `cmd/<app>/` holds entry points; keep `main.go` thin
- `internal/` holds application code
- Tests live next to the code (`foo_test.go`)

## Go conventions
- Never ignore a returned error; handle it or return it
- Pass `context.Context` as the first parameter of anything that does I/O
- Tests are table-driven
- No global mutable state; inject dependencies through constructors
- Prefer the standard library. Ask before adding a new dependency (exception: `github.com/rm4n0s/errors`, see below)

## Error handling
Use `github.com/rm4n0s/errors` for all errors. Never use `fmt.Errorf`, and never import the standard library `errors` package: this package re-exports `errors.Is`, `errors.As` and `errors.Unwrap`, so a single import covers everything.

**Creating errors**
- New failure with a fixed message: `errors.New("Tag", "message", "key", value, ...)`
  - Tag: short PascalCase name of the failure, e.g. `UserAlreadyExists`
  - Trailing arguments are key/value metadata pairs, e.g. `"email", user.Email`
- New failure with a formatted message: `errors.NewErrf("Tag", "format %s", arg)`
- Wrapping an error that is NOT already an `*errors.Error` (stdlib, database driver, third-party): `errors.NewErr("Tag", err)`. Add metadata by chaining `.SetMetadata("key", value)`
- Every `New`, `NewErr` and `NewErrf` call captures the stack at that point

**Propagating errors**
- If the error already came from this package, return it untouched: `return err`. Re-wrapping it would replace the Tag and the stack and break route tests
- Functions return the plain `error` interface, never `*errors.Error` (avoids the non-nil-interface-holding-nil-pointer bug)
- `errors.FromError(err)` is a plain type assertion. It fails if something else wrapped the error, so don't wrap these errors with anything

**Inspecting errors**
- `appErr, ok := errors.FromError(err)`, then use `appErr.Tag`, `appErr.Metadata`, `appErr.Message`
- Use `errors.Is` / `errors.As` / `errors.Unwrap` for the underlying cause stored in `OriginalErr`

**Testing errors**
- Every new failure case gets its own unique Tag and a test that asserts the route, not just that an error occurred:
  ```go
  appErr, ok := errors.FromError(err)
  if !ok {
      t.Fatalf("expected *errors.Error, got %T", err)
  }
  if !appErr.HasRoute("UserService.CreateUser->UserRepository.Save.UserAlreadyExists") {
      t.Errorf("unexpected failure path: %s", appErr.Route())
  }
  ```
- A route lists the call chain from outermost to innermost (`Type.Method` or `Func`, joined by `->`) and ends with the Tag. `HasRoute` matches by substring
- Log errors with `appErr.ToJson()` so they can be replayed in a test via `errors.FromJson`

## Git and GitHub
- Never commit or push directly to `main`; work on a branch named `feat/...` or `fix/...`
- Commit messages: imperative mood, e.g. "Add retry to client"
- Use the `gh` CLI for PRs, issues and CI: `gh pr create`, `gh pr view`, `gh run list`
- PRs: short description, link the issue (`Closes #123`), keep them small
- CI runs in GitHub Actions (`.github/workflows/`); make sure it passes before marking a PR ready

## Don't
- Don't edit generated files (`*.pb.go`, `*_gen.go`); regenerate them with `make generate`
- Don't commit secrets or `.env` files
## Project notes
- Only `internal/ui` and `internal/camera` import miqt; keep new logic in Qt-free packages so it tests without Qt. Design and decisions are in `docs/SPEC.md`
- Building needs Qt 6 (with Multimedia) and FFmpeg dev files with libvpx; the first miqt compile takes ~10 minutes
- miqt memory: objects from `New*` constructors need `Delete()`; values returned by value (`ToImage`, `Copy`, `ConvertToFormat`, device lists) already have a finalizer. Free images with `qtutil.Release`, never `Delete()`, and read device lists inside `qtutil.WithGCPaused`
- Qt objects may only be touched from the main thread; from other goroutines use `mainthread.Start`/`Wait`
- Qt tests set `QT_QPA_PLATFORM=offscreen`; each test binary can create only one `QApplication`
