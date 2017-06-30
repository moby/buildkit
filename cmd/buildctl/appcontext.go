package main

import (
	"context"
	"os"
	"os/signal"
	"sync"

	"golang.org/x/sys/unix"
)

var appContextCache context.Context
var appContextOnce sync.Once

func appContext() context.Context {
	appContextOnce.Do(func() {
		signals := make(chan os.Signal, 2048)
		signal.Notify(signals, unix.SIGTERM, unix.SIGINT)

		const exitLimit = 3
		retries := 0

		ctx, cancel := context.WithCancel(context.Background())
		appContextCache = ctx

		go func() {
			for {
				<-signals
				cancel()
				retries++
				if retries >= exitLimit {
					os.Exit(1)
				}
			}
		}()
	})
	return appContextCache
}
