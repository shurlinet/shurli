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
		names = append(names, cmd.Name)
	}
	sb.WriteString("# Plugin commands\n")
	sb.WriteString("_shurli_plugin_commands=\"" + strings.Join(names, " ") + "\"\n\n")

	// Case branches for each command
	for _, cmd := range cmds {
		sb.WriteString(fmt.Sprintf("        %s)\n", cmd.Name))

		if len(cmd.Subcommands) > 0 {
			var subNames []string
			for _, sub := range cmd.Subcommands {
				subNames = append(subNames, sub.Name)
			}
			sb.WriteString(fmt.Sprintf("            if [[ $COMP_CWORD -eq 2 ]]; then\n"))
			sb.WriteString(fmt.Sprintf("                COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", strings.Join(subNames, " ")))
			sb.WriteString("                return\n")
			sb.WriteString("            fi\n")

			// Flags for subcommands
			for _, sub := range cmd.Subcommands {
				if len(sub.Flags) > 0 {
					sb.WriteString(fmt.Sprintf("            if [[ \"${COMP_WORDS[2]}\" == \"%s\" ]]; then\n", sub.Name))
					var flagNames []string
					for _, f := range sub.Flags {
						flagNames = append(flagNames, "--"+f.Long)
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
				flagNames = append(flagNames, "--"+f.Long)
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
	for _, cmd := range cmds {
		desc := strings.ReplaceAll(cmd.Description, "'", "'\\''")
		sb.WriteString(fmt.Sprintf("        '%s:%s'\n", cmd.Name, desc))
	}
	sb.WriteString("\n")

	// Subcommand/flag completions
	for _, cmd := range cmds {
		sb.WriteString(fmt.Sprintf("    %s)\n", cmd.Name))

		if len(cmd.Subcommands) > 0 {
			sb.WriteString("        local -a subcmds\n        subcmds=(\n")
			for _, sub := range cmd.Subcommands {
				desc := strings.ReplaceAll(sub.Description, "'", "'\\''")
				sb.WriteString(fmt.Sprintf("            '%s:%s'\n", sub.Name, desc))
			}
			sb.WriteString("        )\n")
			sb.WriteString("        _describe 'subcommand' subcmds\n")
		}

		if len(cmd.Flags) > 0 {
			sb.WriteString("        _arguments \\\n")
			for i, f := range cmd.Flags {
				trail := " \\"
				if i == len(cmd.Flags)-1 {
					trail = ""
				}
				if f.Type == "bool" {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]'%s\n", f.Long, f.Description, trail))
				} else if f.Type == "enum" && len(f.Enum) > 0 {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:value:(%s)'%s\n",
						f.Long, f.Description, strings.Join(f.Enum, " "), trail))
				} else if f.Type == "file" {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:file:_files'%s\n", f.Long, f.Description, trail))
				} else if f.Type == "directory" {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:directory:_directories'%s\n", f.Long, f.Description, trail))
				} else {
					sb.WriteString(fmt.Sprintf("            '--%s[%s]:value:'%s\n", f.Long, f.Description, trail))
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
		// Top-level command completion
		sb.WriteString(fmt.Sprintf("complete -c shurli -n __fish_use_subcommand -a %s -d '%s'\n",
			cmd.Name, escapeFish(cmd.Description)))

		// Subcommand completions
		for _, sub := range cmd.Subcommands {
			sb.WriteString(fmt.Sprintf("complete -c shurli -n '__fish_seen_subcommand_from %s' -a %s -d '%s'\n",
				cmd.Name, sub.Name, escapeFish(sub.Description)))

			for _, f := range sub.Flags {
				line := fmt.Sprintf("complete -c shurli -n '__fish_seen_subcommand_from %s' -l %s",
					cmd.Name, f.Long)
				if f.RequiresArg {
					line += " -r"
				}
				if f.Type == "enum" && len(f.Enum) > 0 {
					line += fmt.Sprintf(" -a '%s'", strings.Join(f.Enum, " "))
				}
				line += fmt.Sprintf(" -d '%s'", escapeFish(f.Description))
				sb.WriteString(line + "\n")
			}
		}

		// Top-level flag completions
		for _, f := range cmd.Flags {
			line := fmt.Sprintf("complete -c shurli -n '__fish_seen_subcommand_from %s' -l %s",
				cmd.Name, f.Long)
			if f.Short != "" {
				line += fmt.Sprintf(" -s %s", f.Short)
			}
			if f.RequiresArg {
				line += " -r"
			}
			if f.Type == "enum" && len(f.Enum) > 0 {
				line += fmt.Sprintf(" -a '%s'", strings.Join(f.Enum, " "))
			}
			line += fmt.Sprintf(" -d '%s'", escapeFish(f.Description))
			sb.WriteString(line + "\n")
		}
	}

	return sb.String()
}

// escapeFish escapes single quotes for fish shell strings.
func escapeFish(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// GenerateManSection returns troff-formatted man page section for all registered plugin commands.
func GenerateManSection(cmds []CLICommandEntry) string {
	if len(cmds) == 0 {
		return ""
	}

	var sb strings.Builder

	for _, cmd := range cmds {
		if len(cmd.Subcommands) > 0 {
			for _, sub := range cmd.Subcommands {
				sb.WriteString(fmt.Sprintf(".TP\n\\fB%s %s\\fR", cmd.Name, sub.Name))

				// Add flags
				for _, f := range sub.Flags {
					if f.Type == "bool" {
						sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR]", f.Long))
					} else {
						sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR \\fI%s\\fR]", f.Long, f.Type))
					}
				}
				sb.WriteString("\n")
				sb.WriteString(sub.Description + "\n")
			}
		} else {
			sb.WriteString(fmt.Sprintf(".TP\n\\fB%s\\fR", cmd.Name))

			for _, f := range cmd.Flags {
				if f.Type == "bool" {
					sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR]", f.Long))
				} else {
					sb.WriteString(fmt.Sprintf(" [\\fB--%s\\fR \\fI%s\\fR]", f.Long, f.Type))
				}
			}
			sb.WriteString("\n")
			sb.WriteString(cmd.Description + "\n")
		}
	}

	return sb.String()
}
