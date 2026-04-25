package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Command defines shell command parameters
type Command struct {
	Binary  string
	Args    []string
	Envs    []string
	WorkDir string
	Ctx     context.Context
}

// Run runs a shell command with stdout/stderr connected to the terminal.
// If Command.Ctx is set, the process is killed when the context expires.
func Run(c Command) error {
	if len(c.Binary) == 0 {
		return errors.New("No command specified")
	}

	fullPathBinary, err := exec.LookPath(c.Binary)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	if c.Ctx != nil {
		cmd = exec.CommandContext(c.Ctx, fullPathBinary, c.Args...)
	} else {
		cmd = exec.Command(fullPathBinary, c.Args...)
	}
	cmd.Env = c.Envs
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if c.Ctx != nil && c.Ctx.Err() != nil {
			return fmt.Errorf("command timed out: %w", c.Ctx.Err())
		}
		if exiterr, ok := err.(*exec.ExitError); ok {
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				return fmt.Errorf("shell command exit with code %d", status.ExitStatus())
			}
		}
		return err
	}

	return nil
}

// RunCapture runs a shell command and returns its stdout as bytes.
// Stderr is still connected to the terminal for diagnostics.
func RunCapture(c Command) ([]byte, error) {
	if len(c.Binary) == 0 {
		return nil, errors.New("No command specified")
	}

	fullPathBinary, err := exec.LookPath(c.Binary)
	if err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	if c.Ctx != nil {
		cmd = exec.CommandContext(c.Ctx, fullPathBinary, c.Args...)
	} else {
		cmd = exec.Command(fullPathBinary, c.Args...)
	}
	cmd.Env = c.Envs
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		if c.Ctx != nil && c.Ctx.Err() != nil {
			return nil, fmt.Errorf("command timed out: %w", c.Ctx.Err())
		}
		if exiterr, ok := err.(*exec.ExitError); ok {
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				return nil, fmt.Errorf("shell command exit with code %d", status.ExitStatus())
			}
		}
		return nil, err
	}

	return out, nil
}
