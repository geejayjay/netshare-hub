// Package sysexec is the real implementation of the sys interfaces. It needs
// Linux and root.
package sysexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Command is one program invocation. Args is passed as-is to the program:
// there is no shell, so nothing in it is ever interpreted.
type Command struct {
	Name string
	Args []string
	// Stdin is fed to the program. It may hold secrets and is never logged.
	Stdin string
}

// Result is what a finished program returned.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs commands. Tests replace it to record and script invocations.
type Runner interface {
	// Run returns an error only when the program could not be run at all.
	// A program that ran and failed is reported through Result.ExitCode.
	Run(ctx context.Context, cmd Command) (Result, error)
}

// allowed lists every program this package may run, with its fixed location.
// A program that is not listed cannot be run, whatever a caller passes.
var allowed = map[string][]string{
	"useradd":    {"/usr/sbin/useradd"},
	"userdel":    {"/usr/sbin/userdel"},
	"groupadd":   {"/usr/sbin/groupadd"},
	"getent":     {"/usr/bin/getent"},
	"id":         {"/usr/bin/id"},
	"smbpasswd":  {"/usr/bin/smbpasswd"},
	"pdbedit":    {"/usr/bin/pdbedit"},
	"testparm":   {"/usr/bin/testparm"},
	"smbcontrol": {"/usr/bin/smbcontrol"},
	"smbstatus":  {"/usr/bin/smbstatus"},
	"systemctl":  {"/usr/bin/systemctl", "/bin/systemctl"},
}

const commandTimeout = 30 * time.Second

// ExecRunner runs real programs.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, cmd Command) (Result, error) {
	paths, ok := allowed[cmd.Name]
	if !ok {
		return Result{}, fmt.Errorf("program %q is not on the allow-list", cmd.Name)
	}
	path := ""
	for _, candidate := range paths {
		if _, err := exec.LookPath(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		return Result{}, fmt.Errorf("program %q is not installed (looked in %s)", cmd.Name, strings.Join(paths, ", "))
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	c := exec.CommandContext(ctx, path, cmd.Args...)
	// A fixed, minimal environment: nothing from the server's own environment
	// can change how a system tool behaves, and output is always in English.
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C"}
	c.Stdin = strings.NewReader(cmd.Stdin)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	start := time.Now()
	err := c.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}

	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
		err = nil
	default:
		err = fmt.Errorf("run %s: %w", cmd.Name, err)
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("run %s: timed out after %s", cmd.Name, commandTimeout)
	}

	// Every OS command is logged with its arguments and outcome. Arguments
	// never contain secrets (those go over stdin), so this is safe.
	level := slog.LevelInfo
	if err != nil || res.ExitCode != 0 {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "os command",
		"program", cmd.Name, "args", cmd.Args, "exit_code", res.ExitCode,
		"stderr", truncate(res.Stderr, 2000), "duration_ms", time.Since(start).Milliseconds(), "err", err)

	return res, err
}

// run executes a command and turns a non-zero exit code into an error.
func run(ctx context.Context, r Runner, name string, args ...string) (Result, error) {
	return runInput(ctx, r, "", name, args...)
}

func runInput(ctx context.Context, r Runner, stdin, name string, args ...string) (Result, error) {
	res, err := r.Run(ctx, Command{Name: name, Args: args, Stdin: stdin})
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("%s exited with code %d: %s", name, res.ExitCode, truncate(strings.TrimSpace(res.Stderr), 500))
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
