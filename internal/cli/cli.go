package cli

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ivp/ivp/internal/config"
	"github.com/spf13/cobra"
)

//go:embed default-config.yaml
var templates embed.FS

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }
func ExitCode(err error) int {
	var e exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 6
}

type commands struct {
	configPath               string
	stdout                   io.Writer
	stderr                   io.Writer
	version, commit, builtAt string
}

func Execute(ctx context.Context, version, commit, builtAt string) error {
	c := &commands{stdout: os.Stdout, stderr: os.Stderr, version: version, commit: commit, builtAt: builtAt}
	root := &cobra.Command{Use: "ivp", Short: "Incremental Verification Planner", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&c.configPath, "config", ".ivp/config.yaml", "repository configuration file")
	root.AddCommand(c.initCommand(), c.doctorCommand(), c.indexCommand(), c.planCommand(), c.verifyCommand(), c.runCommand(), c.reportCommand(), c.auditCommand(), c.policyCommand(), c.versionCommand())
	root.SetOut(c.stdout)
	root.SetErr(c.stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		return err
	}
	return nil
}

func (c *commands) initCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "init", Short: "Create a repository configuration template", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if _, err := os.Stat(c.configPath); err == nil && !force {
			return exitError{2, fmt.Errorf("%s already exists; use --force to replace it", c.configPath)}
		}
		if err := os.MkdirAll(filepath.Dir(c.configPath), 0o700); err != nil {
			return exitError{2, err}
		}
		data, err := templates.ReadFile("default-config.yaml")
		if err != nil {
			return err
		}
		if err = os.WriteFile(c.configPath, data, 0o600); err != nil {
			return exitError{2, err}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created %s. Add an explicit generic full-suite argv before using the generic adapter.\n", c.configPath)
		return nil
	}}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing config")
	return cmd
}

func (c *commands) doctorCommand() *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Check local safety prerequisites and fallback instructions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadFile(c.configPath)
		if err != nil {
			return exitError{2, err}
		}
		root, err := repositoryRoot(cmd.Context())
		if err != nil {
			return exitError{2, fmt.Errorf("repository root: %w", err)}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Repository: %s\nConfig: %s\nMode: %s\n", root, c.configPath, cfg.Modes.Default)
		fmt.Fprintln(cmd.OutOrStdout(), "Policy: configuration is valid; organization-mandated checks remain protected")
		fmt.Fprintln(cmd.OutOrStdout(), "Selective execution: Windows is unsupported; use the configured full-suite command")
		fallback, why := fallbackInstruction(cfg)
		if fallback == "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Full-suite fallback is not configured: %s\n", why)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Full-suite fallback argv: %s\n", fallback)
		}
		for _, issue := range cfg.ReadinessIssues() {
			fmt.Fprintf(cmd.OutOrStdout(), "Readiness issue: %s\n", issue)
		}
		if cfg.Server.Endpoint != "" && os.Getenv("IVP_TOKEN") == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "API auth: IVP_TOKEN is not set; offline mode will use only the local full-suite fallback")
		}
		if strings.EqualFold(runtime.GOOS, "windows") {
			return exitError{3, errors.New("unsupported selective execution environment; run the full-suite fallback above")}
		}
		if fallback == "" {
			return exitError{3, errors.New("safe full-suite fallback is not configured")}
		}
		return nil
	}}
}

func (c *commands) policyCommand() *cobra.Command {
	root := &cobra.Command{Use: "policy", Short: "Inspect local policy", Args: cobra.NoArgs}
	root.AddCommand(&cobra.Command{Use: "validate", Short: "Validate effective policy and protected gates", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadFile(c.configPath)
		if err != nil {
			return exitError{2, err}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Policy configuration is valid.\nMode: %s\nProtected checks: %s\nHard full-suite path rules: %d\nRisk-based CI: disabled until organization approval and calibration.\n", cfg.Modes.Default, strings.Join(cfg.Safety.AlwaysRunCheckIDs, ", "), len(cfg.Safety.HardFullSuitePaths))
		return nil
	}})
	return root
}

func (c *commands) versionCommand() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print build and adapter version information", Args: cobra.NoArgs, Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "ivp %s\ncommit: %s\nbuilt: %s\ngo: %s\nadapters: gradle, maven, bazel, nx, pytest, jest, generic\n", c.version, c.commit, c.builtAt, runtime.Version())
	}}
}

func (c *commands) indexCommand() *cobra.Command {
	var full bool
	cmd := &cobra.Command{Use: "index", Short: "Discover and upload repository graph metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return c.withConfig(cmd.Context(), func(cfg config.Config) error { return c.index(cmd.Context(), cmd.OutOrStdout(), cfg, full) })
	}}
	cmd.Flags().BoolVar(&full, "full", false, "rebuild adapter evidence rather than reuse cached discovery")
	return cmd
}
func (c *commands) planCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "plan", Short: "Create an explainable verification plan", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		base, _ := cmd.Flags().GetString("base")
		head, _ := cmd.Flags().GetString("head")
		mode, _ := cmd.Flags().GetString("mode")
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")
		if strings.TrimSpace(base) == "" {
			return exitError{2, errors.New("--base is required")}
		}
		return c.withConfig(cmd.Context(), func(cfg config.Config) error {
			_, err := c.requestPlan(cmd.Context(), cmd.OutOrStdout(), cfg, base, head, mode, format, output)
			return err
		})
	}}
	cmd.Flags().String("base", "", "base Git reference (required)")
	cmd.Flags().String("head", "", "head Git reference (defaults to current HEAD and worktree)")
	cmd.Flags().String("mode", "", "FULL, OBSERVE, FAST_FEEDBACK, or organization-approved RISK_BASED_CI")
	cmd.Flags().String("format", "text", "output format: text or json")
	cmd.Flags().String("output", "", "output file")
	return cmd
}
func (c *commands) verifyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "verify", Short: "Plan and execute verification with safe fallback", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		base, _ := cmd.Flags().GetString("base")
		mode, _ := cmd.Flags().GetString("mode")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		report, _ := cmd.Flags().GetString("report")
		if strings.TrimSpace(base) == "" {
			return exitError{2, errors.New("--base is required")}
		}
		return c.withConfig(cmd.Context(), func(cfg config.Config) error {
			plan, err := c.requestPlan(cmd.Context(), cmd.OutOrStdout(), cfg, base, "", mode, "text", "")
			if err != nil {
				return err
			}
			if dryRun {
				return c.validateCommands(cfg, plan)
			}
			fullSuite, _ := cmd.Flags().GetBool("full-suite")
			return c.runPlan(cmd.Context(), cmd.OutOrStdout(), cfg, plan, report, fullSuite)
		})
	}}
	cmd.Flags().String("base", "", "base Git reference (required)")
	cmd.Flags().String("mode", "", "plan mode")
	cmd.Flags().Bool("dry-run", false, "validate planning and command materialization without running test commands")
	cmd.Flags().String("report", "", "report output path")
	cmd.Flags().Bool("full-suite", false, "run the configured full suite for a comparable audit")
	return cmd
}
func (c *commands) runCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "run", Short: "Revalidate and execute a saved plan", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		reference, _ := cmd.Flags().GetString("plan")
		if strings.TrimSpace(reference) == "" {
			return exitError{2, errors.New("--plan is required")}
		}
		return c.withConfig(cmd.Context(), func(cfg config.Config) error {
			plan, err := c.loadPlan(cmd.Context(), cfg, reference)
			if err != nil {
				return err
			}
			fullSuite, _ := cmd.Flags().GetBool("full-suite")
			return c.runPlan(cmd.Context(), cmd.OutOrStdout(), cfg, plan, "", fullSuite)
		})
	}}
	cmd.Flags().String("plan", "", "plan ID or JSON file (required)")
	cmd.Flags().Bool("full-suite", false, "run the configured full suite even when the plan selected a subset")
	return cmd
}
func (c *commands) reportCommand() *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "report [run-id-or-file]", Short: "Render a verification report", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return renderReport(cmd, args[0], format) }}
	cmd.Flags().StringVar(&format, "format", "text", "text, json, or sarif")
	return cmd
}
func (c *commands) auditCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "Compare a selected run with an equivalent full-suite run", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		run, _ := cmd.Flags().GetString("run")
		if run == "" {
			return exitError{2, errors.New("--run is required")}
		}
		fullRun, _ := cmd.Flags().GetString("full-run")
		adapter, _ := cmd.Flags().GetString("adapter")
		scope, _ := cmd.Flags().GetString("rule-scope")
		return c.withConfig(cmd.Context(), func(cfg config.Config) error {
			return c.audit(cmd.Context(), cmd.OutOrStdout(), cfg, run, fullRun, adapter, scope)
		})
	}}
	cmd.Flags().String("run", "", "run identifier (required)")
	cmd.Flags().String("full-run", "", "completed full-suite report file or execution UUID; omit to run the saved plan as full-suite")
	cmd.Flags().String("adapter", "", "adapter scope (defaults to the selected plan's adapter or all)")
	cmd.Flags().String("rule-scope", "all", "rule scope to quarantine on a possible or confirmed miss")
	return cmd
}

func (c *commands) withConfig(ctx context.Context, fn func(config.Config) error) error {
	cfg, err := config.LoadFile(c.configPath)
	if err != nil {
		return exitError{2, err}
	}
	return fn(cfg)
}
func repositoryRoot(ctx context.Context) (string, error) {
	return gitOutput(ctx, "rev-parse", "--show-toplevel")
}
func gitOutput(ctx context.Context, args ...string) (string, error) {
	return runOutput(ctx, "git", args...)
}
func runOutput(ctx context.Context, executable string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func fallbackInstruction(cfg config.Config) (string, string) {
	argv := cfg.FullSuiteArgv()
	if len(argv) == 0 {
		return "", "set execution.full_suite_argv to the reviewed full verification argv array"
	}
	encoded, err := json.Marshal(argv)
	if err != nil {
		return "", "full-suite argv could not be encoded"
	}
	return string(encoded), ""
}
