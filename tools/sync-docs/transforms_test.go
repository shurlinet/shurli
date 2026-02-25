package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripFirstHeading(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"strips h1", "# Title\n\nBody text", "\nBody text"},
		{"no heading", "Body text\nMore text", "Body text\nMore text"},
		{"only heading", "# Title", ""},
		{"h2 not stripped", "## Subtitle\nBody", "## Subtitle\nBody"},
		{"heading with content below", "# Shurli\n\n## Quick Start\n\nHello", "\n## Quick Start\n\nHello"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripFirstHeading(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteDocLinks(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"plain FAQ", "[see FAQ](FAQ.md)", "[see FAQ](../faq/)"},
		{"FAQ with anchor", "[see FAQ](FAQ.md#section-name)", "[see FAQ](../faq/#section-name)"},
		{"architecture", "[arch](ARCHITECTURE.md)", "[arch](../architecture/)"},
		{"architecture with anchor", "[arch](ARCHITECTURE.md#daemon-architecture)", "[arch](../architecture/#daemon-architecture)"},
		{"roadmap", "[rm](ROADMAP.md)", "[rm](../roadmap/)"},
		{"testing", "[test](TESTING.md)", "[test](../testing/)"},
		{"daemon-api", "[api](DAEMON-API.md)", "[api](../daemon-api/)"},
		{"network-tools", "[nt](NETWORK-TOOLS.md)", "[nt](../network-tools/)"},
		{"monitoring", "[mon](MONITORING.md)", "[mon](../monitoring/)"},
		{"external unchanged", "[g](https://google.com)", "[g](https://google.com)"},
		{"not in map", "[x](RANDOM.md)", "[x](RANDOM.md)"},
		{"inline text unchanged", "See FAQ.md for details", "See FAQ.md for details"},
		{"multiple links", "[a](FAQ.md) and [b](TESTING.md)", "[a](../faq/) and [b](../testing/)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteDocLinks(tt.input)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRewriteImagePaths(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"svg image", "![alt](images/foo.svg)", "![alt](/images/docs/foo.svg)"},
		{"png image", "![alt](images/bar.png)", "![alt](/images/docs/bar.png)"},
		{"no match", "![alt](https://example.com/img.png)", "![alt](https://example.com/img.png)"},
		{"already absolute", "![alt](/images/docs/foo.svg)", "![alt](/images/docs/foo.svg)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteImagePaths(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteSpecialLinks(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{
			"relay readme",
			"See [relay-server/README.md](../relay-server/README.md) for details.",
			"See [Relay Setup guide](../relay-setup/) for details.",
		},
		{
			"engineering journal",
			"See ([`docs/ENGINEERING-JOURNAL.md`](ENGINEERING-JOURNAL.md))",
			"See ([`docs/ENGINEERING-JOURNAL.md`](../engineering-journal/))",
		},
		{"no match", "regular text", "regular text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteSpecialLinks(tt.input)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRewriteGitHubSourceLinks(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"cmd link", "[x](../cmd/shurli/main.go)", "[x](" + githubBase + "/cmd/shurli/main.go)"},
		{"pkg link", "[x](../pkg/p2pnet/foo.go)", "[x](" + githubBase + "/pkg/p2pnet/foo.go)"},
		{"internal link", "[x](../internal/config/)", "[x](" + githubBase + "/internal/config/)"},
		{"github link", "[ci](../.github/workflows/ci.yml)", "[ci](" + githubBase + "/.github/workflows/ci.yml)"},
		{"not relative", "[x](cmd/shurli/main.go)", "[x](cmd/shurli/main.go)"},
		{"external", "[x](https://example.com)", "[x](https://example.com)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteGitHubSourceLinks(tt.input)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRewriteQuickStartLinks(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{
			"relay readme friendly",
			"[relay-server/README.md](relay-server/README.md)",
			"[Relay Setup guide](../relay-setup/)",
		},
		{"docs link", "[x](docs/FAQ.md)", "[x](" + githubBase + "/docs/FAQ.md)"},
		{"cmd link", "[x](cmd/shurli/)", "[x](" + githubBase + "/cmd/shurli/)"},
		{"external", "[x](https://example.com)", "[x](https://example.com)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteQuickStartLinks(tt.input)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRewriteJournalBackticks(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"pkg ref", "`pkg/p2pnet/interfaces.go`", "`" + githubBase + "/pkg/p2pnet/interfaces.go`"},
		{"cmd ref", "`cmd/shurli/main.go`", "`" + githubBase + "/cmd/shurli/main.go`"},
		{"internal ref", "`internal/config/loader.go`", "`" + githubBase + "/internal/config/loader.go`"},
		{"no backtick", "pkg/p2pnet/interfaces.go", "pkg/p2pnet/interfaces.go"},
		{"other prefix", "`test/docker/file.go`", "`test/docker/file.go`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteJournalBackticks(tt.input)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRewriteJournalMdToDir(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"simple", "(core-architecture.md)", "(core-architecture/)"},
		{"with hyphens", "(batch-a-reliability.md)", "(batch-a-reliability/)"},
		{"with digits", "(pre-batch-i.md)", "(pre-batch-i/)"},
		{"uppercase not matched", "(FAQ.md)", "(FAQ.md)"},
		{"external not matched", "(https://example.com/foo.md)", "(https://example.com/foo.md)"},
		{"in table", "| [Core Architecture](core-architecture.md) |", "| [Core Architecture](core-architecture/) |"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteJournalMdToDir(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteFaqImagePaths(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"svg image", "![alt](../images/foo.svg)", "![alt](/images/docs/foo.svg)"},
		{"png image", "![alt](../images/bar.png)", "![alt](/images/docs/bar.png)"},
		{"no match", "![alt](https://example.com/img.png)", "![alt](https://example.com/img.png)"},
		{"main doc path unchanged", "![alt](images/foo.svg)", "![alt](images/foo.svg)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteFaqImagePaths(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteFaqDocLinks(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"architecture", "[arch](../ARCHITECTURE.md)", "[arch](../../architecture/)"},
		{"architecture anchor", "[arch](../ARCHITECTURE.md#daemon)", "[arch](../../architecture/#daemon)"},
		{"roadmap", "[rm](../ROADMAP.md)", "[rm](../../roadmap/)"},
		{"faq self-link", "[faq](../FAQ.md)", "[faq](../../faq/)"},
		{"external unchanged", "[g](https://google.com)", "[g](https://google.com)"},
		{"not in map", "[x](../RANDOM.md)", "[x](../RANDOM.md)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteFaqDocLinks(tt.input)
			if got != tt.want {
				t.Errorf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestPromoteHeadings(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"promotes h3", "### Build\n\nContent", "## Build\n\nContent"},
		{"h2 unchanged", "## Build\n\nContent", "## Build\n\nContent"},
		{"h4 unchanged", "#### Build\n\nContent", "#### Build\n\nContent"},
		{"multiple", "### A\n\n### B", "## A\n\n## B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promoteHeadings(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSection(t *testing.T) {
	readme := `# Shurli

Some intro text.

## Quick Start

Build from source:

### Prerequisites

Go 1.26+

## Features

Feature list here.
`
	tests := []struct {
		name, heading, wantContains, wantNotContains string
	}{
		{"quick start found", "Quick Start", "Build from source:", "Feature list"},
		{"includes subsection", "Quick Start", "### Prerequisites", ""},
		{"missing section", "NonExistent", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSection(readme, tt.heading)
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Errorf("expected to contain %q, got:\n%s", tt.wantContains, got)
			}
			if tt.wantNotContains != "" && strings.Contains(got, tt.wantNotContains) {
				t.Errorf("should not contain %q, got:\n%s", tt.wantNotContains, got)
			}
		})
	}
}

func TestBuildFrontMatter(t *testing.T) {
	tests := []struct {
		name, title string
		weight      int
		desc        string
		wantLines   []string
	}{
		{
			"with description",
			"FAQ", 3, "Frequently asked questions.",
			[]string{`title: "FAQ"`, `weight: 3`, `description: "Frequently asked questions."`},
		},
		{
			"without description",
			"Roadmap", 9, "",
			[]string{`title: "Roadmap"`, `weight: 9`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFrontMatter(tt.title, tt.weight, tt.desc)
			if !strings.HasPrefix(got, "---\n") || !strings.HasSuffix(got, "---\n") {
				t.Errorf("missing YAML delimiters:\n%s", got)
			}
			for _, line := range tt.wantLines {
				if !strings.Contains(got, line) {
					t.Errorf("missing line %q in:\n%s", line, got)
				}
			}
			if tt.desc == "" && strings.Contains(got, "description:") {
				t.Error("should not include description when empty")
			}
		})
	}
}

func TestBuildSyncComment(t *testing.T) {
	got := buildSyncComment("docs/FAQ.md")
	want := "<!-- Auto-synced from docs/FAQ.md by sync-docs - do not edit directly -->\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Integration test: full sync with minimal project structure.
func TestRun_FullSync(t *testing.T) {
	root := t.TempDir()

	// Create go.mod
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0644)

	// Create README.md with Quick Start section
	os.WriteFile(filepath.Join(root, "README.md"), []byte(`# Shurli

## Quick Start

### Build

`+"```"+`bash
go build ./cmd/shurli
`+"```"+`

See [relay-server/README.md](relay-server/README.md) for relay setup.

## Features

Feature list.
`), 0644)

	// Create docs/ with minimal files
	docsDir := filepath.Join(root, "docs")
	os.MkdirAll(filepath.Join(docsDir, "images"), 0755)
	os.MkdirAll(filepath.Join(docsDir, "engineering-journal"), 0755)
	os.MkdirAll(filepath.Join(docsDir, "faq"), 0755)

	os.WriteFile(filepath.Join(docsDir, "ARCHITECTURE.md"), []byte("# Architecture\n\nSee the [FAQ](FAQ.md#how-it-works) for comparisons.\n\nFull API in [DAEMON-API.md](DAEMON-API.md).\n"), 0644)
	os.WriteFile(filepath.Join(docsDir, "images", "test.svg"), []byte("<svg/>"), 0644)

	// Create FAQ sub-pages
	os.WriteFile(filepath.Join(docsDir, "faq", "README.md"), []byte("# Shurli FAQ\n\n| [Design Philosophy](design-philosophy.md) | Why no accounts. |\n"), 0644)
	os.WriteFile(filepath.Join(docsDir, "faq", "design-philosophy.md"), []byte("# FAQ - Design Philosophy\n\nSee [ARCHITECTURE.md](../ARCHITECTURE.md) for details.\n\n![diagram](../images/test.svg)\n"), 0644)

	// Create engineering journal
	os.WriteFile(filepath.Join(docsDir, "engineering-journal", "README.md"), []byte("# Engineering Journal\n\n| [Core](core-architecture.md) |\n"), 0644)
	os.WriteFile(filepath.Join(docsDir, "engineering-journal", "core-architecture.md"), []byte("# Core Architecture\n\n**Reference**: `pkg/p2pnet/network.go`\n"), 0644)

	// Create relay-server/README.md
	os.MkdirAll(filepath.Join(root, "relay-server"), 0755)
	os.WriteFile(filepath.Join(root, "relay-server", "README.md"), []byte("# Relay Setup\n\nSetup instructions.\n"), 0644)

	// Create website directory
	os.MkdirAll(filepath.Join(root, "website", "content", "docs"), 0755)
	os.MkdirAll(filepath.Join(root, "website", "static"), 0755)

	// Run sync
	err := run([]string{"--root-dir", root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Verify outputs exist
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "faq", "_index.md"))
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "faq", "design-philosophy.md"))
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "architecture.md"))
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "quick-start.md"))
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "relay-setup.md"))
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "engineering-journal", "_index.md"))
	assertFileExists(t, filepath.Join(root, "website", "content", "docs", "engineering-journal", "core-architecture.md"))
	assertFileExists(t, filepath.Join(root, "website", "static", "images", "docs", "test.svg"))
	assertFileExists(t, filepath.Join(root, "website", "static", "llms-full.txt"))

	// Verify FAQ _index.md (from README.md)
	faqIndex := readFile(t, filepath.Join(root, "website", "content", "docs", "faq", "_index.md"))
	assertContains(t, faqIndex, `title: "FAQ"`, "FAQ index should have title")
	assertContains(t, faqIndex, "(design-philosophy/)", "FAQ index should rewrite .md to directory")
	assertNotContains(t, faqIndex, "# Shurli FAQ", "FAQ index should strip first heading")

	// Verify FAQ sub-page link rewriting
	faqDesign := readFile(t, filepath.Join(root, "website", "content", "docs", "faq", "design-philosophy.md"))
	assertContains(t, faqDesign, "(../../architecture/)", "FAQ sub-page should link to architecture with ../../")
	assertContains(t, faqDesign, "](/images/docs/test.svg)", "FAQ sub-page should rewrite ../images/ path")
	assertNotContains(t, faqDesign, "# FAQ - Design Philosophy", "FAQ sub-page should strip first heading")

	// Verify Architecture link rewriting
	archContent := readFile(t, filepath.Join(root, "website", "content", "docs", "architecture.md"))
	assertContains(t, archContent, "(../faq/#how-it-works)", "Architecture should rewrite FAQ anchor link")
	assertContains(t, archContent, "(../daemon-api/)", "Architecture should rewrite DAEMON-API link")

	// Verify Quick Start
	qsContent := readFile(t, filepath.Join(root, "website", "content", "docs", "quick-start.md"))
	assertContains(t, qsContent, "## Build", "Quick Start should promote ### to ##")
	assertContains(t, qsContent, "[Relay Setup guide](../relay-setup/)", "Quick Start should rewrite relay link")
	assertNotContains(t, qsContent, "Feature list", "Quick Start should not include Features section")

	// Verify Journal _index.md
	indexContent := readFile(t, filepath.Join(root, "website", "content", "docs", "engineering-journal", "_index.md"))
	assertContains(t, indexContent, "(core-architecture/)", "Journal index should rewrite .md to directory")
	assertNotContains(t, indexContent, "core-architecture.md)", "Journal index should not have .md links")

	// Verify Journal entry backtick rewriting
	coreContent := readFile(t, filepath.Join(root, "website", "content", "docs", "engineering-journal", "core-architecture.md"))
	assertContains(t, coreContent, "`"+githubBase+"/pkg/p2pnet/network.go`", "Journal should rewrite backtick refs")
}

func TestRun_DryRun(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0644)
	os.MkdirAll(filepath.Join(root, "docs", "faq"), 0755)
	os.WriteFile(filepath.Join(root, "docs", "faq", "README.md"), []byte("# FAQ\n\nContent.\n"), 0644)
	os.MkdirAll(filepath.Join(root, "website", "content", "docs"), 0755)

	err := run([]string{"--root-dir", root, "--dry-run"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// No output files should be created
	_, err = os.Stat(filepath.Join(root, "website", "content", "docs", "faq", "_index.md"))
	if err == nil {
		t.Error("dry-run should not create files")
	}
}

func TestRun_MissingRoot(t *testing.T) {
	err := run([]string{"--root-dir", "/tmp/nonexistent-shurli-test-12345"})
	if err == nil {
		t.Error("expected error for missing root")
	}
}

// Helpers

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not found: %s", filepath.Base(path))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, content, substr, msg string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Errorf("%s: expected %q in content", msg, substr)
	}
}

func assertNotContains(t *testing.T, content, substr, msg string) {
	t.Helper()
	if strings.Contains(content, substr) {
		t.Errorf("%s: unexpected %q in content", msg, substr)
	}
}
