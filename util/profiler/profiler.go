package profiler

import (
	"context"

	"github.com/pkg/profile"
	"github.com/urfave/cli/v3"
)

func Attach(app *cli.Command) {
	app.Flags = append(app.Flags,
		&cli.StringFlag{
			Name:   "profile-cpu",
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   "profile-memory",
			Hidden: true,
		},
		&cli.IntFlag{
			Name:   "profile-memoryrate",
			Value:  512 * 1024,
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   "profile-block",
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   "profile-mutex",
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   "profile-trace",
			Hidden: true,
		},
	)

	var stoppers = []interface {
		Stop()
	}{}

	before := app.Before
	app.Before = func(ctx context.Context, clicontext *cli.Command) (context.Context, error) {
		if before != nil {
			var err error
			ctx, err = before(ctx, clicontext)
			if err != nil {
				return ctx, err
			}
		}

		if cpuProfile := clicontext.String("profile-cpu"); cpuProfile != "" {
			stoppers = append(stoppers, profile.Start(profile.CPUProfile, profile.ProfilePath(cpuProfile), profile.NoShutdownHook))
		}

		if memProfile := clicontext.String("profile-memory"); memProfile != "" {
			stoppers = append(stoppers, profile.Start(profile.MemProfile, profile.ProfilePath(memProfile), profile.NoShutdownHook, profile.MemProfileRate(clicontext.Int("profile-memoryrate"))))
		}

		if blockProfile := clicontext.String("profile-block"); blockProfile != "" {
			stoppers = append(stoppers, profile.Start(profile.BlockProfile, profile.ProfilePath(blockProfile), profile.NoShutdownHook))
		}

		if mutexProfile := clicontext.String("profile-mutex"); mutexProfile != "" {
			stoppers = append(stoppers, profile.Start(profile.MutexProfile, profile.ProfilePath(mutexProfile), profile.NoShutdownHook))
		}

		if traceProfile := clicontext.String("profile-trace"); traceProfile != "" {
			stoppers = append(stoppers, profile.Start(profile.TraceProfile, profile.ProfilePath(traceProfile), profile.NoShutdownHook))
		}
		return ctx, nil
	}

	after := app.After
	app.After = func(ctx context.Context, clicontext *cli.Command) error {
		if after != nil {
			if err := after(ctx, clicontext); err != nil {
				return err
			}
		}

		for _, stopper := range stoppers {
			stopper.Stop()
		}
		return nil
	}
}
