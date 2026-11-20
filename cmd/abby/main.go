// Command abby is the Abby CLI. Client commands use only the public HTTP
// API through sdk/go/abby. The serve, migrate and admin commands are
// operator tools for the reference server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/abby-protocol/abby/sdk/go/abby"
)

// Exit codes (spec/cli.md).
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitAuth        = 3
	exitNotFound    = 4
	exitConflict    = 5
	exitUnavailable = 6
	exitTimeout     = 7
	exitResync      = 8
)

type exitErr struct {
	code int
	err  error
}

func (e *exitErr) Error() string { return e.err.Error() }

func exitCode(err error) int {
	var ee *exitErr
	if errors.As(err, &ee) {
		return ee.code
	}
	var p *abby.Problem
	if errors.As(err, &p) {
		switch {
		case p.Status == 401 || p.Status == 403:
			return exitAuth
		case p.Status == 404:
			return exitNotFound
		case p.Code == "resync_required" || p.Status == 410:
			return exitResync
		case p.Status == 409 || p.Status == 412 || p.Status == 428:
			return exitConflict
		case p.Status == 400 || p.Status == 413:
			return exitUsage
		case p.Status >= 500 || p.Status == 429:
			return exitUnavailable
		}
		return exitError
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return exitUnavailable
	}
	return exitError
}

func main() {
	root := &cobra.Command{
		Use:           "abby",
		Short:         "Abby: coordination for agents, people, and services",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	addGlobalFlags(root)
	root.AddCommand(serverCmds()...)
	root.AddCommand(clientCmds()...)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "abby:", err)
		code := exitCode(err)
		if code == exitError && isUsageErr(err) {
			code = exitUsage
		}
		os.Exit(code)
	}
}

func isUsageErr(err error) bool {
	var ue *usageErr
	return errors.As(err, &ue)
}

type usageErr struct{ msg string }

func (u *usageErr) Error() string { return u.msg }

func usage(format string, a ...any) error { return &usageErr{fmt.Sprintf(format, a...)} }
