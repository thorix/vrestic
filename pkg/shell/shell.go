package shell

import (
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
}

// Run runs a shell command with stdout/stderr connected to the terminal
func Run(Command Command) error {
	if len(Command.Binary) == 0 {
		return errors.New("No command specified")
	}

	fullPathBinary, err := exec.LookPath(Command.Binary)
	if err != nil {
		return err
	}

	cmd := exec.Command(fullPathBinary, Command.Args...)
	cmd.Env = Command.Envs
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				return fmt.Errorf("shell command exit with code %d", status.ExitStatus())
			}
		}
		return err
	}

	return nil
}
