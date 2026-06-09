package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// markdownInputFlags provides a consistent, shell-safe way to pass markdown
// bodies to Linear mutations. Inline flags are fine for short prose; file/stdin
// variants are the safe path for agent-written markdown containing shell code,
// expansions, quotes, or multi-line formatting.
type markdownInputFlags struct {
	inlineFlag string
	fileFlag   string
	stdinFlag  string

	inline string
	file   string
	stdin  bool
}

// Keep these declarations literal so the shipped SKILL verifier can match
// documented flags back to Cobra source while the resolver stays shared.
func addDescriptionInputFlags(cmd *cobra.Command, f *markdownInputFlags, inlineUsage string) {
	f.inlineFlag = "description"
	f.fileFlag = "description-file"
	f.stdinFlag = "description-stdin"

	cmd.Flags().StringVar(&f.inline, "description", "", inlineUsage)
	cmd.Flags().StringVar(&f.file, "description-file", "", "Read markdown from file; use '-' to read from stdin")
	cmd.Flags().BoolVar(&f.stdin, "description-stdin", false, "Read markdown from stdin")
}

func addBodyInputFlags(cmd *cobra.Command, f *markdownInputFlags, inlineUsage string) {
	f.inlineFlag = "body"
	f.fileFlag = "body-file"
	f.stdinFlag = "body-stdin"

	cmd.Flags().StringVar(&f.inline, "body", "", inlineUsage)
	cmd.Flags().StringVar(&f.file, "body-file", "", "Read markdown from file; use '-' to read from stdin")
	cmd.Flags().BoolVar(&f.stdin, "body-stdin", false, "Read markdown from stdin")
}

func addContentInputFlags(cmd *cobra.Command, f *markdownInputFlags, inlineUsage string) {
	f.inlineFlag = "content"
	f.fileFlag = "content-file"
	f.stdinFlag = "content-stdin"

	cmd.Flags().StringVar(&f.inline, "content", "", inlineUsage)
	cmd.Flags().StringVar(&f.file, "content-file", "", "Read markdown from file; use '-' to read from stdin")
	cmd.Flags().BoolVar(&f.stdin, "content-stdin", false, "Read markdown from stdin")
}

func (f markdownInputFlags) resolve(cmd *cobra.Command, required bool) (string, bool, error) {
	set, err := f.selectedSources(cmd, required)
	if err != nil || len(set) == 0 {
		return "", false, err
	}

	switch set[0] {
	case "--" + f.inlineFlag:
		return f.inline, true, nil
	case "--" + f.fileFlag:
		if f.file == "" {
			return "", false, usageErr(fmt.Errorf("--%s requires a file path", f.fileFlag))
		}
		if f.file == "-" {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return "", false, fmt.Errorf("reading --%s from stdin: %w", f.fileFlag, err)
			}
			return string(data), true, nil
		}
		data, err := os.ReadFile(f.file)
		if err != nil {
			return "", false, fmt.Errorf("reading --%s %q: %w", f.fileFlag, f.file, err)
		}
		return string(data), true, nil
	case "--" + f.stdinFlag:
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", false, fmt.Errorf("reading --%s: %w", f.stdinFlag, err)
		}
		return string(data), true, nil
	default:
		return "", false, fmt.Errorf("internal error: unexpected markdown source %q", set[0])
	}
}

func (f markdownInputFlags) resolveDryRun(cmd *cobra.Command, required bool) (string, bool, error) {
	set, err := f.selectedSources(cmd, required)
	if err != nil || len(set) == 0 {
		return "", false, err
	}
	if set[0] == "--"+f.stdinFlag || (set[0] == "--"+f.fileFlag && f.file == "-") {
		return "", true, nil
	}
	return f.resolve(cmd, required)
}

func (f markdownInputFlags) createValueSet(cmd *cobra.Command, value string, set bool) bool {
	return set && (value != "" || !cmd.Flags().Changed(f.inlineFlag))
}

func (f markdownInputFlags) selectedSources(cmd *cobra.Command, required bool) ([]string, error) {
	set := make([]string, 0, 3)
	if cmd.Flags().Changed(f.inlineFlag) {
		set = append(set, "--"+f.inlineFlag)
	}
	if cmd.Flags().Changed(f.fileFlag) {
		set = append(set, "--"+f.fileFlag)
	}
	if cmd.Flags().Changed(f.stdinFlag) && f.stdin {
		set = append(set, "--"+f.stdinFlag)
	}
	if len(set) > 1 {
		return nil, usageErr(fmt.Errorf("choose only one of %s, %s, or %s", "--"+f.inlineFlag, "--"+f.fileFlag, "--"+f.stdinFlag))
	}
	if len(set) == 0 {
		if required {
			return nil, usageErr(fmt.Errorf("one of %s, %s, or %s is required", "--"+f.inlineFlag, "--"+f.fileFlag, "--"+f.stdinFlag))
		}
		return nil, nil
	}
	return set, nil
}
