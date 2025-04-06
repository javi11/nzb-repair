package nzbrepair

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/javi11/nntppool"
	"github.com/javi11/nzb-repair/internal/config"
	"github.com/javi11/nzb-repair/internal/repairnzb"
	"github.com/javi11/nzb-repair/pkg/par2exedownloader"
	"github.com/spf13/cobra"
)

var (
	defaultPar2Exe = "./par2cmd"
	configFile     string
	outputFile     string
	verbose        bool
	rootCmd        = &cobra.Command{
		Use:   "nzbrepair [nzb file]",
		Short: "NZB Repair tool",
		Long:  `A command line tool to repair NZB files`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return execute(cmd.Context(), args[0], outputFile)
		},
	}
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "output file path for the repaired nzb")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	rootCmd.MarkPersistentFlagRequired("config")
}

func execute(ctx context.Context, nzbFile string, outputFile string) error {
	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	cfg, err := config.NewFromFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	uploadPool, err := nntppool.NewConnectionPool(
		nntppool.Config{
			Providers: cfg.UploadProviders,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create upload pool: %w", err)
	}

	// TODO This should be Quit but for some reason it doesn't work
	/* 	defer func() {
		slog.InfoContext(ctx, "Closing upload pool")
		uploadPool.Quit()
	}() */

	downloadPool, err := nntppool.NewConnectionPool(
		nntppool.Config{
			Providers: cfg.DownloadProviders,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create upload pool: %w", err)
	}

	defer func() {
		slog.InfoContext(ctx, "Closing download pool")
		downloadPool.Quit()
	}()

	if cfg.Par2Exe == "" {
		if _, err := os.Stat(defaultPar2Exe); err == nil {
			slog.InfoContext(ctx, "Par2 executable found in default path")

			cfg.Par2Exe = defaultPar2Exe
		} else {
			slog.InfoContext(ctx, "No par2 executable configured, downloading animetosho/par2cmdline-turbo")

			execPath, err := par2exedownloader.DownloadPar2Cmd()
			if err != nil {
				return fmt.Errorf("failed to download par2cmd: %w", err)
			}

			cfg.Par2Exe = execPath
		}
	}

	return repairnzb.RepairNzb(
		ctx,
		cfg,
		downloadPool,
		uploadPool,
		nzbFile,
		outputFile,
	)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
