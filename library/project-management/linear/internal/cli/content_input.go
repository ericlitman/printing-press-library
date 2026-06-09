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

func (f *markdownInputFlags) addFlags(cmd *cobra.Command, inlineFlag, fileFlag, stdinFlag, inlineUsage string) {
	f.inlineFlag = inlineFlag
	f.fileFlag = fileFlag
	f.stdinFlag = stdinFlag

	cmd.Flags().StringVar(&f.inline, inlineFlag, "", inlineUsage)
	cmd.Flags().StringVar(&f.file, fileFlag, "", "Read markdown from file; use '-' to read from stdin")
	cmd.Flags().BoolVar(&f.stdin, stdinFlag, false, "Read markdown from stdin")
}

func (f markdownInputFlags) resolve(cmd *cobra.Command, required bool) (string, bool, error) {
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
		return "", false, usageErr(fmt.Errorf("choose only one of %s, %s, or %s", "--"+f.inlineFlag, "--"+f.fileFlag, "--"+f.stdinFlag))
	}
	if len(set) == 0 {
		if required {
			return "", false, usageErr(fmt.Errorf("one of %s, %s, or %s is required", "--"+f.inlineFlag, "--"+f.fileFlag, "--"+f.stdinFlag))
		}
		return "", false, nil
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
