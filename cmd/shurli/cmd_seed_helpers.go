package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/validate"
)

// stdinReader buffers non-TTY stdin reads. Package-level to prevent
// double-buffering when readPassword is called multiple times (password + confirm).
var stdinReader *bufio.Reader

// stdinReadLine reads a single line from stdin without terminal echo.
// Used when stdin is not a TTY (piped input, systemd, Docker).
func stdinReadLine() (string, error) {
	if stdinReader == nil {
		stdinReader = bufio.NewReader(os.Stdin)
	}
	line, err := stdinReader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// zeroBytes securely zeroes a byte slice. Used to clear seed material
// in CLI commands after key derivation completes.
func zeroBytes(b []byte) {
	subtle.XORBytes(b, b, b)
}

// readPassword prompts for a password with no echo. Returns the password string.
// When stdin is not a TTY (piped input, systemd, Docker), reads a line from
// stdin directly. This follows the standard Unix pattern used by ssh-keygen,
// gpg, and openssl for non-interactive password input.
func readPassword(prompt string, stdout io.Writer) (string, error) {
	fmt.Fprint(stdout, prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		pw, err := term.ReadPassword(fd)
		fmt.Fprintln(stdout) // newline after hidden input
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		return string(pw), nil
	}
	// Non-TTY stdin (piped input, systemd, Docker): read line directly.
	// No echo suppression needed - there is no terminal to echo on.
	line, err := stdinReadLine()
	if err != nil {
		return "", fmt.Errorf("reading password from stdin: %w", err)
	}
	return line, nil
}

// strengthLabel returns a colorized strength indicator for terminal display.
func strengthLabel(s validate.PasswordStrength) string {
	switch s {
	case validate.PasswordWeak:
		return "\033[31m[weak]\033[0m" // red
	case validate.PasswordFair:
		return "\033[33m[fair]\033[0m" // yellow
	case validate.PasswordStrong:
		return "\033[32m[strong]\033[0m" // green
	default:
		return ""
	}
}

// readPasswordWithStrength reads a password character-by-character in raw mode,
// showing a live strength indicator that updates as the user types.
// Falls back to readPassword + post-check for non-TTY stdin.
func readPasswordWithStrength(prompt string, stdout io.Writer) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Non-TTY: no live feedback possible, read normally.
		pw, err := readPassword(prompt, stdout)
		if err != nil {
			return "", err
		}
		strength := validate.CheckPasswordStrength(pw)
		if !validate.PasswordAcceptable(pw) {
			return "", fmt.Errorf("password too weak (%s): need at least 3 of: uppercase, lowercase, digit, symbol", strength)
		}
		return pw, nil
	}

	// Save terminal state and switch to raw mode.
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("setting raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	var pw []byte
	buf := make([]byte, 1)

	// Print initial prompt with empty strength.
	fmt.Fprintf(stdout, "%s", prompt)

	for {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			return "", fmt.Errorf("reading input: %w", err)
		}

		b := buf[0]

		switch {
		case b == '\r' || b == '\n': // Enter
			// Clear the strength label and move to next line.
			fmt.Fprintf(stdout, "\033[2K\r%s\r\n", prompt)
			return string(pw), nil

		case b == 3: // Ctrl+C
			fmt.Fprintf(stdout, "\r\n")
			return "", fmt.Errorf("interrupted")

		case b == 4: // Ctrl+D (EOF)
			fmt.Fprintf(stdout, "\r\n")
			return "", fmt.Errorf("interrupted")

		case b == 127 || b == 8: // Backspace / Delete
			if len(pw) > 0 {
				pw = pw[:len(pw)-1]
			}

		default:
			// Only accept printable ASCII (32-126).
			if b >= 32 && b <= 126 {
				pw = append(pw, b)
			}
		}

		// Update the strength indicator on the same line.
		label := ""
		if len(pw) > 0 {
			label = " " + strengthLabel(validate.CheckPasswordStrength(string(pw)))
		}
		fmt.Fprintf(stdout, "\033[2K\r%s%s", prompt, label)
	}
}

// readPasswordConfirm prompts for a password with live strength feedback,
// then asks for confirmation. Rejects weak passwords.
func readPasswordConfirm(prompt, confirmPrompt string, stdout io.Writer) (string, error) {
	pw, err := readPasswordWithStrength(prompt, stdout)
	if err != nil {
		return "", err
	}

	if len(pw) < validate.MinPasswordLen {
		return "", fmt.Errorf("password must be at least %d characters", validate.MinPasswordLen)
	}

	if !validate.PasswordAcceptable(pw) {
		return "", fmt.Errorf("password is too weak: need at least 3 of: uppercase, lowercase, digit, symbol")
	}

	confirm, err := readPassword(confirmPrompt, stdout)
	if err != nil {
		return "", err
	}

	if pw != confirm {
		return "", fmt.Errorf("passwords do not match")
	}

	return pw, nil
}

// confirmSeedBackup quizzes the user on 3 random words from their seed phrase.
// Returns nil on success, error on failure (max 3 total attempts).
func confirmSeedBackup(stdout io.Writer, stdin io.Reader, words []string, skip bool) error {
	if skip {
		return nil
	}

	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Pick 3 random positions (1-indexed, non-repeating).
		positions, err := pickRandomPositions(len(words), 3)
		if err != nil {
			return err
		}

		allCorrect := true
		for _, pos := range positions {
			fmt.Fprintf(stdout, "Enter word #%d: ", pos+1)
			var answer string
			fmt.Fscanln(stdin, &answer)
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != strings.ToLower(words[pos]) {
				fmt.Fprintf(stdout, "Incorrect. Expected %q.\n", words[pos])
				allCorrect = false
				break
			}
		}

		if allCorrect {
			return nil
		}

		if attempt < maxAttempts-1 {
			fmt.Fprintln(stdout, "\nYour seed phrase again:")
			fmt.Fprintf(stdout, "\n  %s\n\n", strings.Join(words, " "))
		}
	}

	return fmt.Errorf("seed backup confirmation failed after %d attempts", maxAttempts)
}

// pickRandomPositions picks n unique random positions from [0, total).
func pickRandomPositions(total, n int) ([]int, error) {
	if n > total {
		n = total
	}
	positions := make([]int, 0, n)
	used := make(map[int]bool)

	for len(positions) < n {
		big, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
		if err != nil {
			return nil, err
		}
		pos := int(big.Int64())
		if !used[pos] {
			used[pos] = true
			positions = append(positions, pos)
		}
	}
	return positions, nil
}

// readSeedPhrase prompts for a BIP39 seed phrase with no echo.
// The input is hidden (like password entry) to prevent shoulder surfing
// and avoid exposing the mnemonic in terminal scrollback.
// Falls back to line-based stdin reading when stdin is not a TTY.
func readSeedPhrase(stdout io.Writer) (string, error) {
	fmt.Fprint(stdout, "Seed phrase (hidden): ")
	fd := int(os.Stdin.Fd())
	var phrase string
	if term.IsTerminal(fd) {
		phraseBytes, err := term.ReadPassword(fd)
		fmt.Fprintln(stdout) // newline after hidden input
		if err != nil {
			return "", fmt.Errorf("reading seed phrase: %w", err)
		}
		phrase = strings.TrimSpace(string(phraseBytes))
	} else {
		line, err := stdinReadLine()
		if err != nil {
			return "", fmt.Errorf("reading seed phrase from stdin: %w", err)
		}
		phrase = strings.TrimSpace(line)
	}
	if phrase == "" {
		return "", fmt.Errorf("seed phrase cannot be empty")
	}
	return phrase, nil
}

// resolvePassword gets the identity password from the session token.
// Returns ("", error) if no session token exists or is invalid.
func resolvePassword(configDir string) (string, error) {
	pw, err := identity.LoadSession(configDir)
	if err != nil {
		return "", fmt.Errorf("session token error: %w", err)
	}
	if pw == "" {
		return "", fmt.Errorf("no session token found; run 'shurli init' to create an identity")
	}
	return pw, nil
}

// resolvePasswordFromConfig resolves the identity password by first finding
// the config file (using the optional configPath flag), then loading the
// session token from the config directory. Used by standalone CLI commands.
func resolvePasswordFromConfig(configPath string) (string, error) {
	cfgFile, err := config.FindConfigFile(configPath)
	if err != nil {
		return "", err
	}
	return resolvePassword(filepath.Dir(cfgFile))
}

// resolvePasswordInteractive tries resolvePassword (session token), then
// falls back to interactive terminal prompt.
// Under systemd/Docker (no TTY), fails with an actionable error instead of
// attempting a doomed stdin read that would EOF immediately.
func resolvePasswordInteractive(configDir string, stdout io.Writer) (string, error) {
	pw, err := resolvePassword(configDir)
	if err == nil {
		return pw, nil
	}

	// No TTY available (systemd, Docker, cron): don't attempt interactive prompt.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no valid session token and no terminal available\n"+
			"  Session error: %v\n"+
			"  Fix: run interactively once to create a session token:\n"+
			"    rm %s/.session\n"+
			"    shurli relay serve --config <config-path>\n"+
			"  Then restart the service.", err, configDir)
	}

	// Fall back to interactive prompt.
	return readPassword("Password: ", stdout)
}
