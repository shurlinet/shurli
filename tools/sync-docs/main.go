// sync-docs transforms docs/*.md into website/content/docs/ with Hugo front matter
// and link rewriting. Dev tooling only - not packaged into releases.
//
// Usage:
//
//	go run ./tools/sync-docs
//	go run ./tools/sync-docs --root-dir /path/to/peer-up
//	go run ./tools/sync-docs --dry-run
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sync-docs", flag.ContinueOnError)
	rootDir := fs.String("root-dir", "", "project root directory (default: auto-detect from go.mod)")
	dryRun := fs.Bool("dry-run", false, "show what would be synced without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := resolveRoot(*rootDir)
	if err != nil {
		return err
	}

	docsDir := filepath.Join(root, "docs")
	websiteDir := filepath.Join(root, "website")
	outDir := filepath.Join(websiteDir, "content", "docs")

	if !*dryRun {
		os.MkdirAll(outDir, 0755)
		os.MkdirAll(filepath.Join(outDir, "engineering-journal"), 0755)
		os.MkdirAll(filepath.Join(outDir, "faq"), 0755)
		os.MkdirAll(filepath.Join(websiteDir, "static", "images", "docs"), 0755)
	}

	count := 0

	// 1. Sync images
	count += syncImages(docsDir, websiteDir, *dryRun)

	// 2. Sync main docs
	fmt.Println("Syncing docs/ -> website/content/docs/")
	for _, entry := range docEntries {
		if syncMainDoc(docsDir, outDir, entry, *dryRun) {
			count++
		}
	}

	// 3. Sync quick-start from README.md
	if syncQuickStart(root, outDir, *dryRun) {
		count++
	}

	// 4. Sync relay setup
	if syncRelaySetup(root, outDir, *dryRun) {
		count++
	}

	// 5. Sync FAQ sub-pages
	faqOutDir := filepath.Join(outDir, "faq")
	for _, entry := range faqEntries {
		if syncFaqEntry(docsDir, faqOutDir, entry, *dryRun) {
			count++
		}
	}

	// 6. Sync engineering journal
	journalOutDir := filepath.Join(outDir, "engineering-journal")
	for _, entry := range journalEntries {
		if syncJournalEntry(docsDir, journalOutDir, entry, *dryRun) {
			count++
		}
	}

	// 7. Generate llms-full.txt
	if generateLLMSFull(root, docsDir, websiteDir, *dryRun) {
		count++
	}

	fmt.Printf("Done. %d files synced.\n", count)
	return nil
}

// resolveRoot finds the project root by looking for go.mod.
func resolveRoot(rootDir string) (string, error) {
	if rootDir != "" {
		abs, err := filepath.Abs(rootDir)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			return "", fmt.Errorf("not a project root (no go.mod): %s", abs)
		}
		return abs, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (no go.mod in any parent directory)")
		}
		dir = parent
	}
}

// syncImages copies docs/images/ -> website/static/images/docs/.
func syncImages(docsDir, websiteDir string, dryRun bool) int {
	srcDir := filepath.Join(docsDir, "images")
	dstDir := filepath.Join(websiteDir, "static", "images", "docs")

	if _, err := os.Stat(srcDir); err != nil {
		return 0
	}

	count := 0
	filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		dstPath := filepath.Join(dstDir, rel)

		if dryRun {
			fmt.Printf("  WOULD SYNC images/%s\n", rel)
			count++
			return nil
		}

		os.MkdirAll(filepath.Dir(dstPath), 0755)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		os.WriteFile(dstPath, data, 0644)
		count++
		return nil
	})

	if count > 0 {
		if dryRun {
			fmt.Printf("  WOULD SYNC docs/images/ -> website/static/images/docs/\n")
		} else {
			fmt.Printf("  SYNC docs/images/ -> website/static/images/docs/\n")
		}
	}
	return count
}

// syncMainDoc syncs a single doc file with all transforms applied.
func syncMainDoc(docsDir, outDir string, entry docEntry, dryRun bool) bool {
	srcPath := filepath.Join(docsDir, entry.Source)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		fmt.Printf("  SKIP %s (not found)\n", entry.Source)
		return false
	}

	body := string(data)
	body = stripFirstHeading(body)
	body = rewriteDocLinks(body)
	body = rewriteImagePaths(body)
	body = rewriteSpecialLinks(body)
	body = rewriteGitHubSourceLinks(body)

	output := buildFrontMatter(entry.Title, entry.Weight, entry.Description)
	output += buildSyncComment("docs/" + entry.Source)
	output += "\n" + body

	if dryRun {
		fmt.Printf("  WOULD SYNC %s -> %s\n", entry.Source, entry.Output)
		return true
	}

	dstPath := filepath.Join(outDir, entry.Output)
	if err := os.WriteFile(dstPath, []byte(output), 0644); err != nil {
		fmt.Printf("  ERROR %s: %v\n", entry.Output, err)
		return false
	}
	fmt.Printf("  SYNC %s -> %s\n", entry.Source, entry.Output)
	return true
}

// syncQuickStart extracts the ## Quick Start section from README.md.
func syncQuickStart(root, outDir string, dryRun bool) bool {
	readmePath := filepath.Join(root, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		fmt.Printf("  SKIP quick-start (README.md not found)\n")
		return false
	}

	body := extractSection(string(data), "Quick Start")
	if body == "" {
		fmt.Printf("  SKIP quick-start (## Quick Start section not found)\n")
		return false
	}

	// Also extract the Disclaimer section and append it
	disclaimer := extractSection(string(data), "Disclaimer")
	if disclaimer != "" {
		body += "\n## Disclaimer\n" + disclaimer
	}

	body = promoteHeadings(body)
	body = rewriteQuickStartLinks(body)

	output := buildFrontMatter(quickStartMeta.Title, quickStartMeta.Weight, quickStartMeta.Description)
	output += buildSyncComment("README.md")
	output += "\n" + body

	if dryRun {
		fmt.Printf("  WOULD SYNC README.md -> quick-start.md\n")
		return true
	}

	dstPath := filepath.Join(outDir, "quick-start.md")
	if err := os.WriteFile(dstPath, []byte(output), 0644); err != nil {
		fmt.Printf("  ERROR quick-start.md: %v\n", err)
		return false
	}
	fmt.Printf("  SYNC README.md -> quick-start.md\n")
	return true
}

// syncRelaySetup syncs relay-server/README.md.
func syncRelaySetup(root, outDir string, dryRun bool) bool {
	srcPath := filepath.Join(root, "relay-server", "README.md")
	data, err := os.ReadFile(srcPath)
	if err != nil {
		fmt.Printf("  SKIP relay-setup (relay-server/README.md not found)\n")
		return false
	}

	body := stripFirstHeading(string(data))

	output := buildFrontMatter(relaySetupMeta.Title, relaySetupMeta.Weight, relaySetupMeta.Description)
	output += buildSyncComment("relay-server/README.md")
	output += "\n" + body

	if dryRun {
		fmt.Printf("  WOULD SYNC relay-server/README.md -> relay-setup.md\n")
		return true
	}

	dstPath := filepath.Join(outDir, "relay-setup.md")
	if err := os.WriteFile(dstPath, []byte(output), 0644); err != nil {
		fmt.Printf("  ERROR relay-setup.md: %v\n", err)
		return false
	}
	fmt.Printf("  SYNC relay-server/README.md -> relay-setup.md\n")
	return true
}

// syncJournalEntry syncs a single engineering journal file.
func syncJournalEntry(docsDir, journalOutDir string, entry journalEntry, dryRun bool) bool {
	srcPath := filepath.Join(docsDir, "engineering-journal", entry.Source)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		fmt.Printf("  SKIP engineering-journal/%s (not found)\n", entry.Source)
		return false
	}

	body := stripFirstHeading(string(data))
	body = rewriteJournalBackticks(body)

	// README.md becomes _index.md with .md->directory link rewriting
	outName := entry.Source
	if entry.Source == "README.md" {
		outName = "_index.md"
		body = rewriteJournalMdToDir(body)
	}

	output := buildFrontMatter(entry.Title, entry.Weight, entry.Description)
	output += buildSyncComment("docs/engineering-journal/" + entry.Source)
	output += "\n" + body

	if dryRun {
		fmt.Printf("  WOULD SYNC engineering-journal/%s -> engineering-journal/%s\n", entry.Source, outName)
		return true
	}

	dstPath := filepath.Join(journalOutDir, outName)
	if err := os.WriteFile(dstPath, []byte(output), 0644); err != nil {
		fmt.Printf("  ERROR engineering-journal/%s: %v\n", outName, err)
		return false
	}
	fmt.Printf("  SYNC engineering-journal/%s -> engineering-journal/%s\n", entry.Source, outName)
	return true
}

// syncFaqEntry syncs a single FAQ sub-page file.
func syncFaqEntry(docsDir, faqOutDir string, entry journalEntry, dryRun bool) bool {
	srcPath := filepath.Join(docsDir, "faq", entry.Source)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		fmt.Printf("  SKIP faq/%s (not found)\n", entry.Source)
		return false
	}

	body := stripFirstHeading(string(data))
	body = rewriteFaqImagePaths(body)
	body = rewriteFaqDocLinks(body)

	// README.md becomes _index.md with .md->directory link rewriting
	outName := entry.Source
	if entry.Source == "README.md" {
		outName = "_index.md"
		body = rewriteJournalMdToDir(body)
	}

	output := buildFrontMatter(entry.Title, entry.Weight, entry.Description)
	output += buildSyncComment("docs/faq/" + entry.Source)
	output += "\n" + body

	if dryRun {
		fmt.Printf("  WOULD SYNC faq/%s -> faq/%s\n", entry.Source, outName)
		return true
	}

	dstPath := filepath.Join(faqOutDir, outName)
	if err := os.WriteFile(dstPath, []byte(output), 0644); err != nil {
		fmt.Printf("  ERROR faq/%s: %v\n", outName, err)
		return false
	}
	fmt.Printf("  SYNC faq/%s -> faq/%s\n", entry.Source, outName)
	return true
}

// generateLLMSFull concatenates all docs into website/static/llms-full.txt.
func generateLLMSFull(root, docsDir, websiteDir string, dryRun bool) bool {
	if dryRun {
		fmt.Printf("  WOULD SYNC llms-full.txt\n")
		return true
	}

	var b strings.Builder
	separator := "\n\n---\n\n"

	// README first
	appendFile(&b, filepath.Join(root, "README.md"), separator)

	// Docs in user-journey order
	for _, entry := range docEntries {
		appendFile(&b, filepath.Join(docsDir, entry.Source), separator)
	}

	// FAQ sub-pages in order
	for _, entry := range faqEntries {
		appendFile(&b, filepath.Join(docsDir, "faq", entry.Source), separator)
	}

	// Engineering journal in order
	for _, entry := range journalEntries {
		appendFile(&b, filepath.Join(docsDir, "engineering-journal", entry.Source), separator)
	}

	// Relay setup last
	appendFile(&b, filepath.Join(root, "relay-server", "README.md"), separator)

	outPath := filepath.Join(websiteDir, "static", "llms-full.txt")
	content := b.String()
	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		fmt.Printf("  ERROR llms-full.txt: %v\n", err)
		return false
	}
	fmt.Printf("  SYNC llms-full.txt (%d bytes)\n", len(content))
	return true
}

// appendFile reads a file and appends its content + separator to the builder.
func appendFile(b *strings.Builder, path, separator string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // silent skip, matching bash behavior
	}
	b.Write(data)
	b.WriteString(separator)
}
