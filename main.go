package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	cli "github.com/urfave/cli/v2"
	"github.com/ztrue/tracerr"

	"github.com/GitJournal/git-auto-sync/common"
)

var version = "dev"

var (
	jsonFlag = &cli.BoolFlag{
		Name:  "json",
		Usage: "Print raw JSON",
	}
	allFlag = &cli.BoolFlag{
		Name:  "all",
		Usage: "Apply to every monitored repo",
	}
	followFlag = &cli.BoolFlag{
		Name:    "follow",
		Aliases: []string{"f"},
		Usage:   "Keep printing new output",
	}
)

func main() {
	app := &cli.App{
		Name:                 "git-auto-sync",
		Version:              version,
		Usage:                "Automatically Sync any Git Repo",
		EnableBashCompletion: true,
		Commands: []*cli.Command{
			{
				Name:    "watch",
				Aliases: []string{"monitor", "w", "m"},
				Usage:   "Monitor the current repo for changes in the foreground",
				Action: func(ctx *cli.Context) error {
					repoPath, err := os.Getwd()
					if err != nil {
						return tracerr.Wrap(err)
					}

					repoPath, err = common.RepoRoot(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					cfg, err := common.NewRepoConfig(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					watchCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
					defer stop()

					return common.WatchForChanges(watchCtx, cfg)
				},
			},
			{
				Name:    "sync",
				Aliases: []string{"s"},
				Usage:   "Sync the current repo once, right now",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:    "env",
						Aliases: []string{"e"},
						Usage:   "Env variables to pass",
					},
				},
				Action: func(ctx *cli.Context) error {
					repoPath, err := os.Getwd()
					if err != nil {
						return tracerr.Wrap(err)
					}

					repoPath, err = common.RepoRoot(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					cfg, err := common.NewRepoConfig(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					cfg.Env = append(cfg.Env, ctx.StringSlice("env")...)

					err = common.AutoSync(cfg)
					if err != nil {
						return tracerr.Wrap(err)
					}

					return nil
				},
			},
			{
				Name:      "check",
				Usage:     "Check if a file will be ignored",
				ArgsUsage: "<file>",
				Action: func(ctx *cli.Context) error {
					repoPath, err := os.Getwd()
					if err != nil {
						return tracerr.Wrap(err)
					}

					repoPath, err = common.RepoRoot(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					path := ctx.Args().First()
					if path == "" {
						return errors.New("usage: git-auto-sync check <file>")
					}
					path, err = filepath.Abs(path)
					if err != nil {
						return tracerr.Wrap(err)
					}

					ignored, err := common.ShouldIgnoreFile(repoPath, common.ResolvePath(path))
					if err != nil {
						return tracerr.Wrap(err)
					}
					fmt.Println("Ignored:", ignored)

					return nil
				},
			},
			{
				Name:   "doctor",
				Usage:  "Check git, credentials, repos, the daemon and its LaunchAgent; changes nothing",
				Flags:  []cli.Flag{jsonFlag},
				Action: doctor,
			},
			{
				Name:  "version",
				Usage: "Print the version",
				Action: func(ctx *cli.Context) error {
					cli.VersionPrinter(ctx)
					return nil
				},
			},
			{
				Name:    "daemon",
				Aliases: []string{"d"},
				Usage:   "Interact with the background daemon",
				Subcommands: []*cli.Command{
					{
						Name:   "status",
						Usage:  "Show the Daemon's status",
						Flags:  []cli.Flag{jsonFlag},
						Action: daemonStatus,
					},
					{
						Name:    "list",
						Aliases: []string{"ls"},
						Usage:   "List of repos being auto-synced",
						Flags:   []cli.Flag{jsonFlag},
						Action:  daemonList,
					},
					{
						Name:      "add",
						Usage:     "Add a repo for auto-sync",
						ArgsUsage: "[repo...]",
						Action:    daemonAdd,
					},
					{
						Name:      "remove",
						Aliases:   []string{"rm"},
						Usage:     "Remove a repo from auto-sync",
						ArgsUsage: "<repo>...",
						Action:    daemonRm,
					},
					{
						Name:      "sync",
						Usage:     "Ask the daemon to sync a repo now",
						ArgsUsage: "[repo...]",
						Flags: []cli.Flag{
							allFlag,
							&cli.BoolFlag{
								Name:  "force",
								Usage: "Retry even if the last attempt ended in an unchanged conflict",
							},
						},
						Action: daemonSync,
					},
					{
						Name:      "pause",
						Usage:     "Pause auto-sync for a repo",
						ArgsUsage: "[repo...]",
						Flags:     []cli.Flag{allFlag},
						Action:    daemonPause,
					},
					{
						Name:      "resume",
						Usage:     "Resume auto-sync for a paused repo",
						ArgsUsage: "[repo...]",
						Flags:     []cli.Flag{allFlag},
						Action:    daemonResume,
					},
					{
						Name:      "events",
						Usage:     "Show recent daemon events",
						ArgsUsage: "[repo]",
						Flags: []cli.Flag{
							&cli.IntFlag{
								Name:    "n",
								Aliases: []string{"limit"},
								Value:   20,
								Usage:   "Number of events to show (0 for all)",
							},
							followFlag,
							jsonFlag,
						},
						Action: daemonEvents,
					},
					{
						Name:  "logs",
						Usage: "Show the daemon log",
						Flags: []cli.Flag{
							&cli.IntFlag{
								Name:  "n",
								Value: 50,
								Usage: "Number of lines to show (0 for all)",
							},
							followFlag,
						},
						Action: daemonLogs,
					},
					{
						Name:      "env",
						Usage:     "Show or set environment variables the daemon passes to git",
						ArgsUsage: "[key=value...]",
						Action:    daemonEnv,
					},
					{
						Name:  "enable",
						Usage: "Install and start the launch agent (auto start at login)",
						Action: func(c *cli.Context) error {
							srv, err := common.NewService()
							if err != nil {
								return tracerr.Wrap(err)
							}
							return tracerr.Wrap(srv.Enable())
						},
					},
					{
						Name:  "disable",
						Usage: "Stop and uninstall the launch agent",
						Action: func(c *cli.Context) error {
							srv, err := common.NewService()
							if err != nil {
								return tracerr.Wrap(err)
							}
							return tracerr.Wrap(srv.Disable())
						},
					},
					{
						Name:  "start",
						Usage: "Start the daemon if installed",
						Action: func(c *cli.Context) error {
							srv, err := common.NewService()
							if err != nil {
								return tracerr.Wrap(err)
							}
							return tracerr.Wrap(srv.Service.Start())
						},
					},
					{
						Name:  "stop",
						Usage: "Stop the daemon if running (keeps installed)",
						Action: func(c *cli.Context) error {
							srv, err := common.NewService()
							if err != nil {
								return tracerr.Wrap(err)
							}
							return tracerr.Wrap(srv.Service.Stop())
						},
					},
					{
						Name:  "restart",
						Usage: "Restart the daemon if running",
						Action: func(c *cli.Context) error {
							srv, err := common.NewService()
							if err != nil {
								return tracerr.Wrap(err)
							}
							_ = srv.Service.Stop()
							return tracerr.Wrap(srv.Service.Start())
						},
					},
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
