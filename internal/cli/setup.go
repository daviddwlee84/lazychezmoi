package cli

import (
	"fmt"
	"os"

	"github.com/daviddwlee84/lazychezmoi/internal/config"
	"github.com/daviddwlee84/lazychezmoi/internal/shell"
	"github.com/spf13/cobra"
)

func (a *app) configCommand() *cobra.Command {
	group := &cobra.Command{Use: "config", Short: "Inspect or initialize lazychezmoi preferences"}
	path := func() (string, error) {
		if a.path != "" {
			return a.path, nil
		}
		return config.Path()
	}
	group.AddCommand(&cobra.Command{Use: "path", Short: "Print the selected preferences path", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := path()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
		return err
	}})
	var jsonMode bool
	show := &cobra.Command{Use: "show", Short: "Print effective preferences", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, _, err := a.settings(cmd)
		if err != nil {
			return err
		}
		if jsonMode {
			return jsonOut(cmd.OutOrStdout(), cfg)
		}
		body, err := config.Encode(cfg)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}}
	show.Flags().BoolVar(&jsonMode, "json", false, "emit structured JSON")
	group.AddCommand(show)
	group.AddCommand(&cobra.Command{Use: "init", Short: "Create preferences without overwriting an existing file", Args: args(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := path()
		if err != nil {
			return err
		}
		if err := config.Create(p); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
		return err
	}})
	return group
}

func shellCommand() *cobra.Command {
	return &cobra.Command{
		Use: "shell-init bash|zsh|fish|powershell", Short: "Print parent-shell integration for explicit reload requests",
		Args: args(cobra.ExactArgs(1)), ValidArgsFunction: enum("bash", "zsh", "fish", "powershell"),
		RunE: func(cmd *cobra.Command, values []string) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			text, err := shell.Generate(values[0], executable)
			if err != nil {
				return usageError{err}
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), text)
			return err
		},
	}
}

func completionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use: "completion bash|zsh|fish|powershell", Short: "Generate shell completion without probing chezmoi",
		Args: args(cobra.ExactArgs(1)), ValidArgsFunction: enum("bash", "zsh", "fish", "powershell"),
		RunE: func(cmd *cobra.Command, values []string) error {
			switch values[0] {
			case "bash":
				return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
			default:
				return usage("unsupported shell %q; choose bash, zsh, fish, or powershell", values[0])
			}
		},
	}
}
