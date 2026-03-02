package main

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/shurlinet/shurli/internal/identity"
)

// zeroBytes securely zeroes a byte slice. Used to clear seed material
// in CLI commands after key derivation completes.
func zeroBytes(b []byte) {
	subtle.XORBytes(b, b, b)
}

const minPasswordLen = 8

// readPassword prompts for a password with no echo. Returns the password string.
func readPassword(prompt string, stdout io.Writer) (string, error) {
	fmt.Fprint(stdout, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(stdout) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return string(pw), nil
}

// readPasswordConfirm prompts for a password and confirmation.
// Enforces minimum length.
func readPasswordConfirm(prompt, confirmPrompt string, stdout io.Writer) (string, error) {
	pw, err := readPassword(prompt, stdout)
	if err != nil {
		return "", err
	}

	if len(pw) < minPasswordLen {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordLen)
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
func readSeedPhrase(stdout io.Writer) (string, error) {
	fmt.Fprint(stdout, "Seed phrase (hidden): ")
	phraseBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(stdout) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading seed phrase: %w", err)
	}
	phrase := strings.TrimSpace(string(phraseBytes))
	if phrase == "" {
		return "", fmt.Errorf("seed phrase cannot be empty")
	}
	return phrase, nil
}

// resolvePassword gets the identity password from the session token.
// Returns ("", error) if no session token exists.
func resolvePassword(configDir string) (string, error) {
	if pw, err := identity.LoadSession(configDir); err == nil && pw != "" {
		return pw, nil
	}
	return "", fmt.Errorf("no session token found; run 'shurli init' to create an identity")
}

// resolvePasswordInteractive tries resolvePassword (session token), then
// falls back to interactive terminal prompt.
func resolvePasswordInteractive(configDir string, stdout io.Writer) (string, error) {
	pw, err := resolvePassword(configDir)
	if err == nil {
		return pw, nil
	}

	// Fall back to interactive prompt.
	return readPassword("Password: ", stdout)
}
