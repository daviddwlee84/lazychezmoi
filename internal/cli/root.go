package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
	"github.com/daviddwlee84/lazychezmoi/internal/config"
	"github.com/daviddwlee84/lazychezmoi/internal/shell"
	"github.com/daviddwlee84/lazychezmoi/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type usageError struct{ error }

func usage(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ue usageError
	if errors.As(err, &ue) {
		return 2
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		return 130
	}
	return 1
}

func buildVersion(injected, module string) string {
	if injected != "" && injected != "dev" && injected != "(devel)" {
		return injected
	}
	if module != "" && module != "(devel)" {
		return module
	}
	return "dev"
}

type app struct {
	path, color string
	autoFetch   bool
	options     chezmoi.Options
}

type colorValue struct{ value *string }

func (v colorValue) String() string { return *v.value }
func (v colorValue) Type() string   { return "string" }
func (v colorValue) Set(value string) error {
	if value != "auto" && value != "always" && value != "never" {
		return fmt.Errorf("color must be auto, always, or never")
	}
	*v.value = value
	return nil
}

func New(version string) *cobra.Command {
	if info, ok := debug.ReadBuildInfo(); ok {
		version = buildVersion(version, info.Main.Version)
	}
	a := &app{color: "auto"}
	root := &cobra.Command{
		Use: "lazychezmoi", Short: "A keyboard-first workbench for chezmoi",
		Long:    "Edit, preview, apply and update your dotfiles. Bare lazychezmoi opens the dashboard in an interactive terminal; otherwise it prints help.",
		Version: version, SilenceUsage: true, SilenceErrors: true,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !interactive(cmd) {
				return cmd.Help()
			}
			return a.dashboard(cmd)
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	flags := root.PersistentFlags()
	flags.StringVar(&a.path, "config", "", "lazychezmoi preferences file")
	flags.StringVar(&a.options.ConfigFile, "chezmoi-config", "", "native chezmoi config file")
	flags.StringVarP(&a.options.SourceDir, "source", "S", "", "chezmoi source directory")
	flags.StringVarP(&a.options.DestinationDir, "destination", "D", "", "chezmoi destination directory")
	flags.StringVarP(&a.options.WorkingTree, "working-tree", "W", "", "Git working tree")
	flags.StringVar(&a.options.CacheDir, "chezmoi-cache", "", "native chezmoi cache directory")
	flags.StringVar(&a.options.PersistentState, "persistent-state", "", "native chezmoi state file")
	flags.StringVar(&a.options.Binary, "chezmoi", "", "chezmoi executable")
	flags.StringVar(&a.options.GitBinary, "git", "", "Git executable")
	flags.Var(colorValue{&a.color}, "color", "color: auto, always, never")
	flags.BoolVar(&a.autoFetch, "auto-fetch", true, "fetch once when opening the dashboard")
	root.RegisterFlagCompletionFunc("color", enum("auto", "always", "never"))
	root.AddCommand(&cobra.Command{Use: "tui", Short: "Open the interactive dashboard", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		if !interactive(cmd) {
			return usage("tui requires terminal input and output")
		}
		return a.dashboard(cmd)
	}})
	root.AddCommand(a.statusCommand(), a.filesCommand(false), a.previewCommand(), a.scriptsCommand())
	for _, kind := range []string{"edit", "apply", "fetch", "update", "init", "re-add", "lazygit"} {
		root.AddCommand(a.operationCommand(kind))
	}
	externals := &cobra.Command{Use: "externals", Short: "Manage chezmoi external dependencies"}
	externals.AddCommand(&cobra.Command{Use: "refresh", Short: "Refresh and apply externals, excluding scripts", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		return runOperation(cmd, service, chezmoi.Operation{Kind: "externals"})
	}})
	root.AddCommand(externals, a.configCommand(), shellCommand(), completionCommand(root))
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print build version", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
		return err
	}})
	return root
}

func args(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, values []string) error {
		if err := check(cmd, values); err != nil {
			return usageError{err}
		}
		return nil
	}
}

func enum(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		var matches []string
		for _, value := range values {
			if strings.HasPrefix(value, prefix) {
				matches = append(matches, value)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	}
}

func interactive(cmd *cobra.Command) bool {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := cmd.OutOrStdout().(*os.File)
	return inOK && outOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func (a *app) settings(cmd *cobra.Command) (config.Config, string, error) {
	path := a.path
	if path == "" {
		var err error
		path, err = config.Path()
		if err != nil {
			return config.Config{}, "", err
		}
	}
	cfg, err := config.Load(path, cmd.Flags().Changed("config"))
	if err != nil {
		return cfg, path, usageError{err}
	}
	if cmd.Flags().Changed("color") {
		cfg.Color = a.color
	}
	if cmd.Flags().Changed("auto-fetch") {
		cfg.AutoFetch = a.autoFetch
	}
	if cmd.Flags().Changed("chezmoi") {
		cfg.Tools.Chezmoi = a.options.Binary
	}
	if cmd.Flags().Changed("git") {
		cfg.Tools.Git = a.options.GitBinary
	}
	if err := config.Validate(cfg); err != nil {
		return cfg, path, usageError{err}
	}
	return cfg, path, nil
}

func (a *app) service(cmd *cobra.Command) (*chezmoi.Service, config.Config, error) {
	cfg, _, err := a.settings(cmd)
	if err != nil {
		return nil, cfg, err
	}
	opts := a.options
	if opts.Binary == "" {
		opts.Binary = cfg.Tools.Chezmoi
	}
	if opts.GitBinary == "" {
		opts.GitBinary = cfg.Tools.Git
	}
	return chezmoi.New(opts), cfg, nil
}

func (a *app) dashboard(cmd *cobra.Command) error {
	service, cfg, err := a.service(cmd)
	if err != nil {
		return err
	}
	color := cfg.Color == "always" || cfg.Color == "auto" && os.Getenv("NO_COLOR") == ""
	return tui.Run(cmd.Context(), service, tui.Options{AutoFetch: cfg.AutoFetch, Color: color})
}

func jsonOut(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func (a *app) statusCommand() *cobra.Command {
	var jsonMode bool
	c := &cobra.Command{Use: "status", Short: "Show deployment differences and independent Git status", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		cx, err := service.Resolve(cmd.Context())
		if err != nil {
			return err
		}
		entries, err := service.Entries(cmd.Context(), false)
		if err != nil {
			return err
		}
		git, gitErr := service.GitStatus(cmd.Context())
		out := struct {
			Context  chezmoi.Context   `json:"context"`
			Files    []chezmoi.Entry   `json:"files"`
			Git      chezmoi.GitStatus `json:"git"`
			GitError string            `json:"git_error,omitempty"`
		}{Context: cx, Files: entries, Git: git}
		if gitErr != nil {
			out.GitError = gitErr.Error()
		}
		if jsonMode {
			return jsonOut(cmd.OutOrStdout(), out)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\nDestination: %s\n", cx.SourceDir, cx.DestinationDir)
		if gitErr != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Git: unavailable (%s)\n", gitErr)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Git: %s  ahead %d / behind %d  dirty=%t\n", git.Branch, git.Ahead, git.Behind, git.Dirty)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Drift / Apply  Target")
		for _, e := range entries {
			if e.Drift != "" && e.Drift != " " || e.Pending != "" && e.Pending != " " {
				printEntry(cmd.OutOrStdout(), e)
			}
		}
		return nil
	}}
	c.Flags().BoolVar(&jsonMode, "json", false, "emit structured JSON")
	return c
}

func printEntry(w io.Writer, e chezmoi.Entry) {
	fmt.Fprintf(w, "%1s%1s  %-12s %s\n", e.Drift, e.Pending, e.Kind, e.Relative)
}

func (a *app) filesCommand(scripts bool) *cobra.Command {
	var jsonMode bool
	use := "files"
	if scripts {
		use = "list"
	}
	c := &cobra.Command{Use: use, Short: "List managed entries and their source paths", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		entries, err := service.Entries(cmd.Context(), scripts)
		if err != nil {
			return err
		}
		if entries == nil {
			entries = []chezmoi.Entry{}
		}
		if jsonMode {
			return jsonOut(cmd.OutOrStdout(), entries)
		}
		for _, e := range entries {
			printEntry(cmd.OutOrStdout(), e)
		}
		return nil
	}}
	c.Flags().BoolVar(&jsonMode, "json", false, "emit structured JSON")
	return c
}

func (a *app) previewCommand() *cobra.Command {
	var view string
	c := &cobra.Command{Use: "preview TARGET", Short: "Print source, current, rendered content or native diff", Args: args(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, values []string) error {
		if view != "source" && view != "current" && view != "rendered" && view != "diff" {
			return usage("view must be source, current, rendered, or diff")
		}
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		entry, err := service.Locate(cmd.Context(), values[0])
		if err != nil {
			return err
		}
		content, err := service.Preview(cmd.Context(), entry, view)
		if err != nil {
			return err
		}
		_, err = io.WriteString(cmd.OutOrStdout(), content)
		return err
	}}
	c.Flags().StringVar(&view, "view", "source", "source, current, rendered, or diff")
	c.RegisterFlagCompletionFunc("view", enum("source", "current", "rendered", "diff"))
	return c
}

func runOperation(cmd *cobra.Command, service *chezmoi.Service, operation chezmoi.Operation, extra ...string) error {
	child, err := service.Command(cmd.Context(), operation)
	if err != nil {
		return err
	}
	child.Args = append(child.Args, extra...)
	if !interactive(cmd) && operation.Kind != "lazygit" && operation.Kind != "source" && operation.Kind != "fetch" {
		child.Args = append(child.Args, "--no-tty")
	}
	child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return child.Run()
}

func (a *app) operationCommand(kind string) *cobra.Command {
	var reload, init, prompt, editApply, scripts bool
	var defaults bool
	promptFlags := map[string]*[]string{}
	use := kind
	check := cobra.NoArgs
	if kind == "edit" || kind == "apply" || kind == "re-add" {
		use += " [TARGET...]"
		check = cobra.ArbitraryArgs
	}
	if kind == "re-add" {
		check = cobra.MinimumNArgs(1)
	}
	if kind == "init" {
		use += " [REPO]"
		check = cobra.MaximumNArgs(1)
	}
	descriptions := map[string]string{"edit": "Edit source using chezmoi's configured editor", "apply": "Apply selected files, or all targets when no paths are given", "fetch": "Fetch the tracking remote without pulling or applying", "update": "Pull, initialize new prompts, and apply", "init": "Initialize a repository or regenerate its config", "re-add": "Absorb live changes to ordinary, non-template files", "lazygit": "Open lazygit in the active working tree"}
	c := &cobra.Command{Use: use, Short: descriptions[kind], Args: args(check), RunE: func(cmd *cobra.Command, values []string) error {
		if reload {
			if err := shell.Validate(interactive(cmd)); err != nil {
				return usageError{err}
			}
		}
		if (kind == "edit" || kind == "lazygit") && !interactive(cmd) {
			return usage("%s requires terminal input and output", kind)
		}
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		if kind == "fetch" {
			if interactive(cmd) {
				return runOperation(cmd, service, chezmoi.Operation{Kind: "fetch"})
			}
			return service.Fetch(cmd.Context(), true)
		}
		op := chezmoi.Operation{Kind: kind, Targets: values, Init: init, Prompt: prompt, Apply: editApply || kind == "update", ExcludeScripts: kind == "apply" && len(values) > 0 && !scripts}
		if kind == "edit" && len(values) == 0 {
			op.Kind = "source"
		}
		var extra []string
		for _, name := range []string{"promptBool", "promptString", "promptInt", "promptChoice", "promptMultichoice"} {
			if p, ok := promptFlags[name]; ok {
				for _, value := range *p {
					extra = append(extra, "--"+name, value)
				}
			}
		}
		if defaults {
			extra = append(extra, "--promptDefaults")
		}
		if err := runOperation(cmd, service, op, extra...); err != nil {
			return err
		}
		if reload {
			return shell.Request()
		}
		return nil
	}}
	if kind == "apply" || kind == "update" {
		c.Flags().BoolVar(&reload, "reload", false, "reload the calling shell on success (requires shell-init)")
	}
	if kind == "apply" {
		c.Flags().BoolVar(&scripts, "scripts", false, "include scripts when applying explicit targets")
	}
	if kind == "update" {
		c.Flags().BoolVar(&init, "init", true, "regenerate config and ask newly added prompts")
	}
	if kind == "init" {
		c.Flags().BoolVar(&prompt, "prompt", false, "ask promptOnce questions again")
		c.Flags().BoolVar(&editApply, "apply", false, "apply after successful initialization")
		c.Flags().BoolVar(&defaults, "promptDefaults", false, "explicitly accept native prompt defaults")
		for _, name := range []string{"promptBool", "promptString", "promptInt", "promptChoice", "promptMultichoice"} {
			promptFlags[name] = c.Flags().StringArray(name, nil, "native prompt text=value answer (repeatable)")
		}
	}
	if kind == "edit" {
		c.Flags().BoolVar(&editApply, "apply", false, "apply after the editor succeeds")
	}
	return c
}

func (a *app) scriptsCommand() *cobra.Command {
	group := &cobra.Command{Use: "scripts", Short: "Inspect and reset recorded script execution history"}
	group.AddCommand(a.filesCommand(true))
	var jsonMode bool
	records := &cobra.Command{Use: "records TARGET", Short: "Inspect exact resettable history records", Args: args(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, values []string) error {
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		entry, err := service.Locate(cmd.Context(), values[0])
		if err != nil {
			return err
		}
		items, err := service.ScriptRecords(cmd.Context(), entry)
		if err != nil {
			return err
		}
		if jsonMode {
			if items == nil {
				items = []chezmoi.ScriptRecord{}
			}
			return jsonOut(cmd.OutOrStdout(), items)
		}
		for _, item := range items {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s  %s\n", item.Bucket, item.Key, item.Description)
		}
		return nil
	}}
	records.Flags().BoolVar(&jsonMode, "json", false, "emit structured JSON")
	group.AddCommand(records)
	for _, kind := range []string{"reset", "apply"} {
		var yes, reset bool
		c := &cobra.Command{Use: kind + " TARGET", Short: map[string]string{"reset": "Reset exact recorded history; shared content hashes affect matching scripts", "apply": "Apply one script through chezmoi; configured hooks still run"}[kind], Args: args(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, values []string) error {
			service, _, err := a.service(cmd)
			if err != nil {
				return err
			}
			entry, err := service.Locate(cmd.Context(), values[0])
			if err != nil {
				return err
			}
			if !strings.HasPrefix(entry.Kind, "script") {
				return usage("%s is not a managed script", values[0])
			}
			if kind == "reset" || reset {
				items, err := service.ScriptRecords(cmd.Context(), entry)
				if err != nil {
					return err
				}
				if len(items) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "No recorded history to reset for %s\n", entry.Relative)
				} else if !yes {
					for _, item := range items {
						fmt.Fprintf(cmd.ErrOrStderr(), "%s %s  %s\n", item.Bucket, item.Key, item.Description)
					}
					return usage("review records above and pass --yes to reset history for %s; once hashes may be shared by identical scripts", entry.Relative)
				} else {
					if err := service.ResetScript(cmd.Context(), entry, items); err != nil {
						return err
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "Reset %d recorded entries for %s\n", len(items), entry.Relative)
				}
			}
			if kind == "reset" {
				return nil
			}
			started := time.Now()
			if err := runOperation(cmd, service, chezmoi.Operation{Kind: "script-apply", Targets: []string{entry.Source}}); err != nil {
				if reset {
					return fmt.Errorf("history reset; script apply failed (effects are not rolled back): %w", err)
				}
				return err
			}
			result, err := service.ScriptResult(cmd.Context(), entry, started)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Apply succeeded; execution could not be verified: %v\n", err)
				result = "unverifiable"
			}
			fmt.Fprintln(cmd.OutOrStdout(), result)
			return nil
		}}
		c.Flags().BoolVar(&yes, "yes", false, "confirm resetting the displayed script history")
		if kind == "apply" {
			c.Flags().BoolVar(&reset, "reset", false, "reset recorded history before applying (requires --yes)")
		}
		group.AddCommand(c)
	}
	return group
}
