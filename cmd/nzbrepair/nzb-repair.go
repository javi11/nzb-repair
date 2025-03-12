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
	configFile string
	rootCmd    = &cobra.Command{
		Use:   "nzbrepair [nzb file]",
		Short: "NZB Repair tool",
		Long:  `A command line tool to repair NZB files`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return execute(cmd.Context(), args[0])
		},
	}
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "config file path")
	rootCmd.MarkPersistentFlagRequired("config")
}

func execute(ctx context.Context, nzbFile string) error {
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

	defer uploadPool.Quit()

	downloadPool, err := nntppool.NewConnectionPool(
		nntppool.Config{
			Providers: cfg.DownloadProviders,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create upload pool: %w", err)
	}

	defer downloadPool.Quit()

	if cfg.Par2Exe == "" {
		slog.InfoContext(ctx, "No par2 executable configured, downloading animetosho/par2cmdline-turbo")

		execPath, err := par2exedownloader.DownloadPar2Cmd()
		if err != nil {
			return fmt.Errorf("failed to download par2cmd: %w", err)
		}

		cfg.Par2Exe = execPath
	}

	return repairnzb.RepairNzb(
		ctx,
		cfg,
		downloadPool,
		uploadPool,
		nzbFile,
	)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
