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

// buildDocLinkMap builds the source->slug mapping from docEntries and linkOnlyEntries.
func buildDocLinkMap() map[string]string {
	m := make(map[string]string, len(docEntries)+len(linkOnlyEntries))
	for _, e := range docEntries {
		slug := strings.TrimSuffix(e.Output, ".md")
		m[e.Source] = slug
	}
	for _, e := range linkOnlyEntries {
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
// 1. [relay-server/README.md](../relay-server/README.md) -> [Relay Setup guide](../relay-setup/)
// 2. (ENGINEERING-JOURNAL.md) -> (../engineering-journal/)
func rewriteSpecialLinks(body string) string {
	body = strings.ReplaceAll(body,
		"[relay-server/README.md](../relay-server/README.md)",
		"[Relay Setup guide](../relay-setup/)")
	body = strings.ReplaceAll(body,
		"(ENGINEERING-JOURNAL.md)",
		"(../engineering-journal/)")
	return body
}

// rewriteGitHubSourceLinks rewrites relative source paths like (../cmd/...)
// to full GitHub URLs.
func rewriteGitHubSourceLinks(body string) string {
	for _, dir := range githubSourceDirs {
		old := "(../" + dir
		new := "(" + githubBase + "/" + dir
		body = strings.ReplaceAll(body, old, new)
	}
	return body
}

// rewriteQuickStartLinks rewrites root-relative links from README.md extraction.
// Also handles the special relay-server/README.md friendly link.
func rewriteQuickStartLinks(body string) string {
	body = strings.ReplaceAll(body,
		"[relay-server/README.md](relay-server/README.md)",
		"[Relay Setup guide](../relay-setup/)")
	for _, dir := range quickStartLinkDirs {
		old := "(" + dir
		new := "(" + githubBase + "/" + dir
		body = strings.ReplaceAll(body, old, new)
	}
	return body
}

// rewriteJournalBackticks rewrites backtick code references in journal files.
// e.g., `pkg/p2pnet/foo.go` -> `https://github.com/.../pkg/p2pnet/foo.go`
func rewriteJournalBackticks(body string) string {
	for _, prefix := range []string{"cmd/peerup/", "pkg/p2pnet/", "internal/"} {
		body = strings.ReplaceAll(body, "`"+prefix, "`"+githubBase+"/"+prefix)
	}
	return body
}

// journalMdLinkRe matches (lowercase-name.md) for journal internal links.
var journalMdLinkRe = regexp.MustCompile(`\(([a-z][a-z0-9-]*)\.md\)`)

// rewriteJournalMdToDir rewrites .md links in journal _index to Hugo directory format.
// e.g., (core-architecture.md) -> (core-architecture/)
func rewriteJournalMdToDir(body string) string {
	return journalMdLinkRe.ReplaceAllString(body, "($1/)")
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
