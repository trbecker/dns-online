package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Getenv, SystemResolver(), os.Stdout, os.Stderr))
}

func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	resolver HostResolver,
	stdout io.Writer,
	stderr io.Writer,
) int {
	cfg, err := ParseConfig(args, getenv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "dns-online: configuration error: %v\n", err)
		return 2
	}

	logf := func(format string, a ...any) {
		if !cfg.Quiet {
			fmt.Fprintf(stdout, "dns-online: "+format+"\n", a...)
		}
	}

	if err := WaitForDNS(ctx, cfg, resolver, logf); err != nil {
		fmt.Fprintf(stderr, "dns-online: %v\n", err)
		return 1
	}

	return 0
}
