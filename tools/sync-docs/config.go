package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// docEntry describes a source doc file to sync into website/content/docs/.
type docEntry struct {
	Source      string `yaml:"source"`
	Output      string `yaml:"output"`
	Weight      int    `yaml:"weight"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

// journalEntry describes an engineering journal or FAQ file to sync.
type journalEntry struct {
	Source      string `yaml:"source"`
	Weight      int    `yaml:"weight"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

// linkOnlyEntry describes a doc excluded from sync but needing link rewriting.
type linkOnlyEntry struct {
	Source string `yaml:"source"`
	Output string `yaml:"output"`
}

// syncConfig holds all configuration loaded from sync-docs.yaml.
type syncConfig struct {
	GithubBase        string         `yaml:"github_base"`
	Docs              []docEntry     `yaml:"docs"`
	QuickStart        docEntry       `yaml:"quick_start"`
	RelaySetup        docEntry       `yaml:"relay_setup"`
	Journal           []journalEntry `yaml:"journal"`
	FAQ               []journalEntry `yaml:"faq"`
	LinkOnly          []linkOnlyEntry `yaml:"link_only"`
	GithubSourceDirs  []string       `yaml:"github_source_dirs"`
	QuickStartLinkDirs []string      `yaml:"quick_start_link_dirs"`
}

// loadConfig reads sync-docs.yaml from the tool directory.
func loadConfig(toolDir string) (*syncConfig, error) {
	path := filepath.Join(toolDir, "sync-docs.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg syncConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Convert linkOnly entries to docEntry format for link rewriting compatibility.
	return &cfg, nil
}

// linkOnlyDocEntries converts linkOnly entries to docEntry slice for link rewriting.
func (c *syncConfig) linkOnlyDocEntries() []docEntry {
	entries := make([]docEntry, len(c.LinkOnly))
	for i, lo := range c.LinkOnly {
		entries[i] = docEntry{Source: lo.Source, Output: lo.Output}
	}
	return entries
}
