package config

import "errors"

var (
	// ErrConfigNotFound is returned when no config file is found
	// at the specified path or in any of the search paths.
	ErrConfigNotFound = errors.New("config file not found")

	// ErrConfigVersionTooNew is returned when a config file has a version
	// newer than what this binary supports.
	ErrConfigVersionTooNew = errors.New("config version too new")

	// ErrNoArchive is returned when a rollback is requested but no
	// last-known-good archive exists.
	ErrNoArchive = errors.New("no last-known-good config archive found")

	// ErrCommitConfirmedPending is returned when a commit-confirmed
	// operation is already in progress.
	ErrCommitConfirmedPending = errors.New("commit-confirmed already pending")

	// ErrNoPending is returned when trying to confirm but no
	// commit-confirmed is active.
	ErrNoPending = errors.New("no commit-confirmed pending")
)
