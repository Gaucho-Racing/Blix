package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const (
	airConfigPath = ".air.toml"
	cliBinaryPath = "/tmp/blix-air/blix-cli"
	reloadMessage = "\x1b[38;2;152;147;163m↻ reloading Blix CLI\x1b[0m"
)

type processExit struct {
	command *exec.Cmd
	err     error
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	air := exec.CommandContext(ctx, "air", "-c", airConfigPath)
	air.Stdout = os.Stdout
	air.Stderr = os.Stderr
	if err := air.Start(); err != nil {
		return fmt.Errorf("start Air: %w", err)
	}
	defer stopProcess(air)

	airExited := make(chan error, 1)
	go func() {
		airExited <- air.Wait()
	}()

	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()

	var cli *exec.Cmd
	var cliExited <-chan processExit
	var forceStop <-chan time.Time
	var observedBuild time.Time
	restartPending := false

	for {
		select {
		case <-ctx.Done():
			stopProcess(cli)
			return nil
		case err := <-airExited:
			stopProcess(cli)
			if ctx.Err() != nil || err == nil {
				return nil
			}
			return fmt.Errorf("Air stopped: %w", err)
		case result := <-cliExited:
			if result.command != cli {
				continue
			}
			cli = nil
			cliExited = nil
			forceStop = nil
			if restartPending {
				restartPending = false
				var err error
				cli, cliExited, err = startCLI()
				if err != nil {
					return err
				}
				continue
			}
			if result.err != nil {
				return fmt.Errorf("Blix CLI stopped: %w", result.err)
			}
			return nil
		case <-forceStop:
			forceStop = nil
			if cli != nil && restartPending {
				_ = cli.Process.Kill()
			}
		case <-poll.C:
			build, err := os.Stat(cliBinaryPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("inspect CLI build: %w", err)
			}
			if !build.ModTime().After(observedBuild) {
				continue
			}
			observedBuild = build.ModTime()
			if cli == nil {
				cli, cliExited, err = startCLI()
				if err != nil {
					return err
				}
				continue
			}
			fmt.Fprintln(os.Stderr, "\n"+reloadMessage)
			restartPending = true
			stopProcess(cli)
			forceStop = time.After(500 * time.Millisecond)
		}
	}
}

func startCLI() (*exec.Cmd, <-chan processExit, error) {
	command := exec.Command(cliBinaryPath)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, nil, fmt.Errorf("start Blix CLI: %w", err)
	}
	exited := make(chan processExit, 1)
	go func() {
		exited <- processExit{command: command, err: command.Wait()}
	}()
	return command, exited, nil
}

func stopProcess(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
}
