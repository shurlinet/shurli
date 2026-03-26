package plugin

import (
	"fmt"
	"strings"
)

// GenerateBashCompletion returns bash completion additions for all registered plugin commands.
// Output is inserted into the bash completion script's command list and case branches.
func GenerateBashCompletion(cmds []CLICommandEntry) string {
	if len(cmds) == 0 {
		return ""
	}

	var sb strings.Builder

	// Command names for the top-level commands= string
	var names []string
	for _, cmd := range cmds {
		names = append(names, escapeShellDesc(cmd.Name))
	}
	sb.WriteString("# Plugin commands\n")
	sb.WriteString("_shurli_plugin_commands=\"" + strings.Join(names, " ") + "\"\n\n")

	// Case branches for each command
	// M4 fix: escape flag names in COMPREPLY strings.
	// L6 fix: quote COMP_WORDS comparison values.
	for _, cmd := range cmds {
		sb.WriteString(fmt.Sprintf("        %s)\n", escapeShellDesc(cmd.Name)))

		if len(cmd.Subcommands) > 0 {
			var subNames []string
			for _, sub := range cmd.Subcommands {
				subNames = append(subNames, escapeShellDesc(sub.Name))
			}
			sb.WriteString(fmt.Sprintf("            if [[ $COMP_CWORD -eq 2 ]]; then\n"))
			sb.WriteString(fmt.Sprintf("                COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", strings.Join(subNames, " ")))
			sb.WriteString("                return\n")
			sb.WriteString("            fi\n")

			// Flags for subcommands
			for _, sub := range cmd.Subcommands {
				if len(sub.Flags) > 0 {
					// L6 fix: quote the COMP_WORDS[2] comparison value.
					sb.WriteString(fmt.Sprintf("            if [[ \"${COMP_WORDS[2]}\" == \"%s\" ]]; then\n", escapeShellDesc(sub.Name)))
					var flagNames []string
					for _, f := range sub.Flags {
						flagNames = append(flagNames, "--"+escapeShellDesc(f.Long))
					}
					sb.WriteString(fmt.Sprintf("                COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", strings.Join(flagNames, " ")))
					sb.WriteString("                return\n")
					sb.WriteString("            fi\n")
				}
			}
		}

		// Top-level flags
		if len(cmd.Flags) > 0 {
			var flagNames []string
			for _, f := range cmd.Flags {
				flagNames = append(flagNames, "--"+escapeShellDesc(f.Long))
			}
			sb.WriteString(fmt.Sprintf("            COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", strings.Join(flagNames, " ")))
		}
		sb.WriteString("            ;;\n")
	}

	return sb.String()
}

// GenerateZshCompletion returns zsh completion additions for all registered plugin commands.
func GenerateZshCompletion(cmds []CLICommandEntry) string {
	if len(cmds) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Plugin commands\n")

	// Command descriptions for _describe
	// M3 fix: escape zsh-dangerous chars in descriptions (brackets, colons, backslashes).
	for _, cmd := range cmds {
		desc := escapeZshDesc(cmd.Description)
		sb.WriteString(fmt.Sprintf("        '%s:%s'\n", cmd.Name, desc))
	}
	sb.WriteString("\n")

	// Subcommand/flag completions
	for _, cmd := range cmds {
		sb.WriteString(fmt.Sprintf("    %s)\n", cmd.Name))

		if len(cmd.Subcommands) > 0 {
			sb.WriteString("        local -a subcmds\n        subcmds=(\n")
			for _, sub := range cmd.Subcommands {
				desc := escapeZshDesc(sub.Description)
				sb.WriteString(fmt.Sprintf("            '%s:%s'\n", sub.Name, desc))
			}
			sb.WriteString("        )\n")
			sb.WriteString("        _describe 'subcommand' subcmds\n")
		}

		// L5 fix: escape flag descriptions inside _arguments bracket syntax.
		if len(cmd.Flags) > 0 {
			sb.WriteString("        _arguments \\\n")
			for i, f := range cmd.Flags {
				trail := " \\"
				if i == len(cmd.Flags)-1 {
					trail = ""
				}
				desc := escapeZshDesc(f.Description)
				if f.Type == "bool" {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]'%s\n", f.Long, desc, trail))
				} else if f.Type == "enum" && len(f.Enum) > 0 {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:value:(%s)'%s\n",
						f.Long, desc, strings.Join(f.Enum, " "), trail))
				} else if f.Type == "file" {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:file:_files'%s\n", f.Long, desc, trail))
				} else if f.Type == "directory" {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:directory:_directories'%s\n", f.Long, desc, trail))
				} else {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:value:'%s\n", f.Long, desc, trail))
				}
			}
		}
		sb.WriteString("        ;;\n")
	}

	return sb.String()
}

// GenerateFishCompletion returns fish completion additions for all registered plugin commands.
func GenerateFishCompletion(cmds []CLICommandEntry) string {
	if len(cmds) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Plugin commands\n")

	for _, cmd := range cmds {
		// Top-level command completion (L3 fix: quote -a argument)
		sb.WriteString(fmt.Sprintf("complete -c shurli -n __fish_use_subcommand -a '%s' -d '%s'\n",
			escapeFish(cmd.Name), escapeFish(escapeShellDesc(cmd.Description))))

		// Subcommand completions
		for _, sub := range cmd.Subcommands {
			sb.WriteString(fmt.Sprintf("complete -c shurli -n '__fish_seen_subcommand_from %s' -a '%s' -d '%s'\n",
				escapeFish(cmd.Name), escapeFish(sub.Name), escapeFish(escapeShellDesc(sub.Description))))

			for _, f := range sub.Flags {
				// L4 fix: quote flag long names with escapeFish.
				line := fmt.Sprintf("complete -c shurli -n '__fish_seen_subcommand_from %s' -l '%s'",
					escapeFish(cmd.Name), escapeFish(f.Long))
				if f.RequiresArg {
					line += " -r"
				}
				if f.Type == "enum" && len(f.Enum) > 0 {
					line += fmt.Sprintf(" -a '%s'", escapeFish(strings.Join(f.Enum, " ")))
				}
				line += fmt.Sprintf(" -d '%s'", escapeFish(f.Description))
				sb.WriteString(line + "\n")
			}
		}

		// Top-level flag completions
		for _, f := range cmd.Flags {
			// L4 fix: quote flag long names with escapeFish.
			line := fmt.Sprintf("complete -c shurli -n '__fish_seen_subcommand_from %s' -l '%s'",
				escapeFish(cmd.Name), escapeFish(f.Long))
			if f.Short != "" {
				line += fmt.Sprintf(" -s '%s'", escapeFish(f.Short))
			}
			if f.RequiresArg {
				line += " -r"
			}
			if f.Type == "enum" && len(f.Enum) > 0 {
				line += fmt.Sprintf(" -a '%s'", escapeFish(strings.Join(f.Enum, " ")))
			}
			line += fmt.Sprintf(" -d '%s'", escapeFish(f.Description))
			sb.WriteString(line + "\n")
		}
	}

	return sb.String()
}

// escapeZshDesc escapes characters dangerous in zsh completion descriptions.
// Handles single quotes (for _describe strings), brackets (for _arguments syntax),
// colons (zsh field separator), and backslashes (M3 + L5 fix).
func escapeZshDesc(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "'", "'\\''")
	s = strings.ReplaceAll(s, "[", "(")
	s = strings.ReplaceAll(s, "]", ")")
	s = strings.ReplaceAll(s, ":", " -")
	return s
}

// escapeFish escapes characters for fish shell single-quoted strings.
// While fish single quotes prevent interpretation, we escape $ and backtick
// to prevent any accidental interpretation in edge cases.
func escapeFish(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "'", "\\'")
	s = strings.ReplaceAll(s, "$", `\$`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// escapeShellDesc strips shell-dangerous characters from descriptions for completion scripts.
// Rather than trying to escape (which still leaves dangerous substrings), we replace them.
func escapeShellDesc(s string) string {
	s = strings.ReplaceAll(s, `\`, `/`)
	s = strings.ReplaceAll(s, `"`, "'")
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "$(", "(")   // strip command substitution prefix
	s = strings.ReplaceAll(s, "${", "{")    // strip variable expansion prefix
	return s
}

// escapeTroff escapes troff special characters in descriptions for man pages.
// Backslashes are doubled, leading dots replaced with escaped form,
// and newlines replaced with spaces to prevent multi-line troff injection.
func escapeTroff(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	// Replace newlines with spaces to prevent multi-line troff injection.
	s = strings.ReplaceAll(s, "\n", " ")
	// Strip ALL leading dots (troff interprets . at start of line as directive).
	s = strings.TrimLeft(s, ".")
	return s
}

// GenerateManSection returns troff-formatted man page section for all registered plugin commands.
func GenerateManSection(cmds []CLICommandEntry) string {
	if len(cmds) == 0 {
		return ""
	}

	var sb strings.Builder

	// M2 fix: escape flag Long names, Type strings, and descriptions with escapeTroff.
	for _, cmd := range cmds {
		if len(cmd.Subcommands) > 0 {
			for _, sub := range cmd.Subcommands {
				sb.WriteString(fmt.Sprintf(".TP\n\\fB%s %s\\fR", escapeTroff(cmd.Name), escapeTroff(sub.Name)))

				// Add flags
				for _, f := range sub.Flags {
					if f.Type == "bool" {
						sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR]", escapeTroff(f.Long)))
					} else {
						sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR \\fI%s\\fR]", escapeTroff(f.Long), escapeTroff(f.Type)))
					}
				}
				sb.WriteString("\n")
				sb.WriteString(escapeTroff(sub.Description) + "\n")
			}
		} else {
			sb.WriteString(fmt.Sprintf(".TP\n\\fB%s\\fR", escapeTroff(cmd.Name)))

			for _, f := range cmd.Flags {
				if f.Type == "bool" {
					sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR]", escapeTroff(f.Long)))
				} else {
					sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR \\fI%s\\fR]", escapeTroff(f.Long), escapeTroff(f.Type)))
				}
			}
			sb.WriteString("\n")
			sb.WriteString(escapeTroff(cmd.Description) + "\n")
		}
	}

	return sb.String()
}
