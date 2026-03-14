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

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// validate checks required fields and detects duplicate sources.
func (c *syncConfig) validate() error {
	if c.GithubBase == "" {
		return fmt.Errorf("github_base is required")
	}
	if c.QuickStart.Source == "" {
		return fmt.Errorf("quick_start.source is required")
	}
	if c.RelaySetup.Source == "" {
		return fmt.Errorf("relay_setup.source is required")
	}

	// Check for duplicate sources within each section.
	// README.md can appear in both journal and FAQ (different directories).
	checkDups := func(section string, sources []string) error {
		seen := make(map[string]bool, len(sources))
		for _, s := range sources {
			if seen[s] {
				return fmt.Errorf("duplicate source %q in %s", s, section)
			}
			seen[s] = true
		}
		return nil
	}

	docSources := make([]string, len(c.Docs))
	for i, e := range c.Docs {
		docSources[i] = e.Source
	}
	if err := checkDups("docs", docSources); err != nil {
		return err
	}

	journalSources := make([]string, len(c.Journal))
	for i, e := range c.Journal {
		journalSources[i] = e.Source
	}
	if err := checkDups("journal", journalSources); err != nil {
		return err
	}

	faqSources := make([]string, len(c.FAQ))
	for i, e := range c.FAQ {
		faqSources[i] = e.Source
	}
	if err := checkDups("faq", faqSources); err != nil {
		return err
	}
	return nil
}

// linkOnlyDocEntries converts linkOnly entries to docEntry slice for link rewriting.
func (c *syncConfig) linkOnlyDocEntries() []docEntry {
	entries := make([]docEntry, len(c.LinkOnly))
	for i, lo := range c.LinkOnly {
		entries[i] = docEntry{Source: lo.Source, Output: lo.Output}
	}
	return entries
}
