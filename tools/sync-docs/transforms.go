package main

import (
	"fmt"
	"regexp"
	"strings"
)

// stripFirstHeading removes the first line if it starts with "# ".
// Hugo renders the title from front matter, so the markdown heading is redundant.
func stripFirstHeading(body string) string {
	if strings.HasPrefix(body, "# ") {
		if idx := strings.IndexByte(body, '\n'); idx >= 0 {
			return body[idx+1:]
		}
		return "" // entire file was just a heading
	}
	return body
}

// buildDocLinkMap builds the source->slug mapping from cfg.Docs and cfg.LinkOnly.
func buildDocLinkMap() map[string]string {
	loEntries := cfg.linkOnlyDocEntries()
	m := make(map[string]string, len(cfg.Docs)+len(loEntries))
	for _, e := range cfg.Docs {
		slug := strings.TrimSuffix(e.Output, ".md")
		m[e.Source] = slug
	}
	for _, e := range loEntries {
		slug := strings.TrimSuffix(e.Output, ".md")
		m[e.Source] = slug
	}
	return m
}

// rewriteDocLinks rewrites cross-references like (FAQ.md) to (../faq/)
// and (FAQ.md#anchor) to (../faq/#anchor).
func rewriteDocLinks(body string) string {
	linkMap := buildDocLinkMap()
	for source, slug := range linkMap {
		// Anchored links first (more specific match)
		body = rewriteDocLinkAnchored(body, source, slug)
		// Plain links
		old := "(" + source + ")"
		new := "(../" + slug + "/)"
		body = strings.ReplaceAll(body, old, new)
	}
	return body
}

// rewriteDocLinkAnchored handles (SOURCE.md#anchor) -> (../slug/#anchor).
func rewriteDocLinkAnchored(body, source, slug string) string {
	// Match (SOURCE.md#anything-until-close-paren)
	prefix := "(" + source + "#"
	for {
		idx := strings.Index(body, prefix)
		if idx < 0 {
			break
		}
		end := strings.IndexByte(body[idx:], ')')
		if end < 0 {
			break
		}
		end += idx
		anchor := body[idx+len(prefix) : end]
		oldLink := body[idx : end+1]
		newLink := "(../" + slug + "/#" + anchor + ")"
		body = strings.Replace(body, oldLink, newLink, 1)
	}
	return body
}

// rewriteImagePaths rewrites ](images/foo.svg) to ](/images/docs/foo.svg).
func rewriteImagePaths(body string) string {
	return strings.ReplaceAll(body, "](images/", "](/images/docs/")
}

// rewriteFaqImagePaths rewrites ](../images/foo.svg) to ](/images/docs/foo.svg).
// FAQ sub-files live in docs/faq/, so images are at ../images/.
func rewriteFaqImagePaths(body string) string {
	return strings.ReplaceAll(body, "](../images/", "](/images/docs/")
}

// rewriteFaqDocLinks rewrites cross-doc references from FAQ sub-pages.
// e.g., (../ARCHITECTURE.md) -> (../../architecture/)
func rewriteFaqDocLinks(body string) string {
	linkMap := buildDocLinkMap()
	for source, slug := range linkMap {
		// Anchored links first
		prefix := "(../" + source + "#"
		for {
			idx := strings.Index(body, prefix)
			if idx < 0 {
				break
			}
			end := strings.IndexByte(body[idx:], ')')
			if end < 0 {
				break
			}
			end += idx
			anchor := body[idx+len(prefix) : end]
			oldLink := body[idx : end+1]
			newLink := "(../../" + slug + "/#" + anchor + ")"
			body = strings.Replace(body, oldLink, newLink, 1)
		}
		// Plain links
		old := "(../" + source + ")"
		newVal := "(../../" + slug + "/)"
		body = strings.ReplaceAll(body, old, newVal)
	}
	return body
}

// rewriteSpecialLinks handles two hardcoded link rewrites:
// 1. [docs/RELAY-SETUP.md](RELAY-SETUP.md) -> [Relay Setup guide](../relay-setup/)
// 2. (ENGINEERING-JOURNAL.md) -> (../engineering-journal/)
func rewriteSpecialLinks(body string) string {
	body = strings.ReplaceAll(body,
		"[docs/RELAY-SETUP.md](RELAY-SETUP.md)",
		"[Relay Setup guide](../relay-setup/)")
	body = strings.ReplaceAll(body,
		"(ENGINEERING-JOURNAL.md)",
		"(../engineering-journal/)")
	return body
}

// rewriteGitHubSourceLinks rewrites relative source paths like (../cmd/...)
// to full GitHub URLs.
func rewriteGitHubSourceLinks(body string) string {
	for _, dir := range cfg.GithubSourceDirs {
		old := "(../" + dir
		new := "(" + cfg.GithubBase + "/" + dir
		body = strings.ReplaceAll(body, old, new)
	}
	return body
}

// rewriteQuickStartLinks rewrites root-relative links from README.md extraction.
// Also handles the special docs/RELAY-SETUP.md friendly link.
func rewriteQuickStartLinks(body string) string {
	body = strings.ReplaceAll(body,
		"[docs/RELAY-SETUP.md](docs/RELAY-SETUP.md)",
		"[Relay Setup guide](../relay-setup/)")
	for _, dir := range cfg.QuickStartLinkDirs {
		old := "(" + dir
		new := "(" + cfg.GithubBase + "/" + dir
		body = strings.ReplaceAll(body, old, new)
	}
	return body
}

// rewriteJournalBackticks rewrites backtick code references in journal files.
// e.g., `pkg/sdk/foo.go` -> `https://github.com/.../pkg/sdk/foo.go`
func rewriteJournalBackticks(body string) string {
	for _, prefix := range []string{"cmd/shurli/", "pkg/sdk/", "internal/", "plugins/"} {
		body = strings.ReplaceAll(body, "`"+prefix, "`"+cfg.GithubBase+"/"+prefix)
	}
	return body
}

// journalMdLinkAnchoredRe matches (lowercase-name.md#anchor) for journal cross-references.
var journalMdLinkAnchoredRe = regexp.MustCompile(`\(([a-z][a-z0-9-]*)\.md(#[^)]+)\)`)

// journalMdLinkPlainRe matches (lowercase-name.md) for journal internal links.
var journalMdLinkPlainRe = regexp.MustCompile(`\(([a-z][a-z0-9-]*)\.md\)`)

// rewriteJournalCrossRefs rewrites .md links between journal entries to Hugo directory format.
// For _index.md (isIndex=true): (name.md) -> (name/), (name.md#anchor) -> (name/#anchor)
// For other entries (isIndex=false): (name.md) -> (../name/), (name.md#anchor) -> (../name/#anchor)
func rewriteJournalCrossRefs(body string, isIndex bool) string {
	prefix := "../"
	if isIndex {
		prefix = ""
	}
	// Anchored links first (more specific match).
	body = journalMdLinkAnchoredRe.ReplaceAllString(body, "("+prefix+"$1/$2)")
	// Plain links.
	body = journalMdLinkPlainRe.ReplaceAllString(body, "("+prefix+"$1/)")
	return body
}

// promoteHeadings replaces lines starting with "### " with "## ".
func promoteHeadings(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "### ") {
			lines[i] = "## " + line[4:]
		}
	}
	return strings.Join(lines, "\n")
}

// extractSection extracts content between "## <heading>" and the next "## " heading.
// Returns the content without the heading line itself.
func extractSection(body string, heading string) string {
	target := "## " + heading
	lines := strings.Split(body, "\n")
	var result []string
	inSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, target) {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if inSection {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return ""
	}
	return strings.TrimRight(strings.Join(result, "\n"), "\n") + "\n"
}

// buildFrontMatter generates Hugo YAML front matter.
func buildFrontMatter(title string, weight int, description string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %q\n", title)
	fmt.Fprintf(&b, "weight: %d\n", weight)
	if description != "" {
		fmt.Fprintf(&b, "description: %q\n", description)
	}
	b.WriteString("---\n")
	return b.String()
}

// buildSyncComment generates the "Auto-synced from" HTML comment.
func buildSyncComment(sourcePath string) string {
	return fmt.Sprintf("<!-- Auto-synced from %s by sync-docs - do not edit directly -->\n", sourcePath)
}
