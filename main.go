package main

import (
	"context"
	"fmt"
	"github.com/daviddwlee84/lazychezmoi/internal/scoopupgrade"
	"os"
	"os/signal"
	"syscall"

	"github.com/daviddwlee84/lazychezmoi/internal/cli"
)

var version = "dev"

func main() {
	if code, handled := scoopupgrade.HandleHelper(scoopupgrade.Product{Binary: "lazychezmoi", Module: "github.com/daviddwlee84/lazychezmoi", Main: "github.com/daviddwlee84/lazychezmoi"}); handled {
		os.Exit(code)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.New(version).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lazychezmoi:", err)
		os.Exit(cli.ExitCode(err))
	}
}
