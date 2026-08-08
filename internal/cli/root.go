package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/spf13/cobra"
)

type App struct {
	Cfg   *config.Config
	Log   *slog.Logger
	Store *store.Store

	closeLog func()
}

func Execute() int {
	var (
		dataDir  string
		outDir   string
		logLevel string
	)
	app := &App{}

	root := &cobra.Command{
		Use:   "halyk-agent",
		Short: "Covenant compliance agent for the Halyk AI Challenge",
		Long: "halyk-agent reads a corpus of opaque financial PDFs plus a transaction ledger\n" +
			"and decides, for every covenant of every borrower, whether it is COMPLIANT or\n" +
			"in BREACH — writing out/submission.json.\n\n" +
			"LLMs extract and classify; Go computes and decides.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if dataDir != "" {
				cfg.DataDir = mustAbs(dataDir)
			}
			if outDir != "" {
				cfg.OutDir = mustAbs(outDir)
			}
			if logLevel != "" {
				cfg.LogLevel = logLevel
			}
			if err := cfg.EnsureDirs(); err != nil {
				return err
			}
			app.Cfg = cfg
			log, closeLog := newLogger(cfg.LogLevel, cfg.LogDir)
			app.Log = log
			app.closeLog = closeLog

			if cmd.Name() == "version" || cmd.Name() == "help" {
				return nil
			}
			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			app.Store = st
			return nil
		},
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if app.Store != nil {
				return app.Store.Close()
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&dataDir, "data", "", "dataset directory (default $DATA_DIR or ./data)")
	root.PersistentFlags().StringVar(&outDir, "out", "", "output directory (default ./out)")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "", "debug|info|warn|error")

	root.AddCommand(
		newIngestCmd(app),
		newTriageCmd(app),
		newCovenantsCmd(app),
		newSpecsCmd(app),
		newFactsCmd(app),
		newClassifyCmd(app),
		newLabelsCmd(app),
		newStabilityCmd(app),
		newEvaluateCmd(app),
		newSubmitCmd(app),
		newReportCmd(app),
		newRunCmd(app),
		newValidateCmd(app),
		newScoreCmd(app),
		newStatusCmd(app),
		newLLMCheckCmd(app),
		newVersionCmd(),
	)

	err := root.Execute()
	if app.closeLog != nil {
		defer app.closeLog()
	}
	if err != nil {
		if app.Log != nil {
			app.Log.Error("command failed", "command", commandPath(root), "err", err)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 1
	}
	return 0
}

func commandPath(root *cobra.Command) string {
	cmd, _, err := root.Find(os.Args[1:])
	if err != nil || cmd == nil {
		return root.Name()
	}
	return cmd.CommandPath()
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
