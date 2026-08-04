package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/plongitudes/beads/internal/git"
	"github.com/plongitudes/beads/internal/ui"
)

// hooksInstalled checks if bd git hooks are installed
func hooksInstalled() bool {
	gitDir, err := git.GetGitDir()
	if err != nil {
		return false
	}
	preCommit := filepath.Join(gitDir, "hooks", "pre-commit")
	postMerge := filepath.Join(gitDir, "hooks", "post-merge")

	// Check if both hooks exist
	_, err1 := os.Stat(preCommit)
	_, err2 := os.Stat(postMerge)

	if err1 != nil || err2 != nil {
		return false
	}

	// Verify they're bd hooks by checking for signature comment
	// #nosec G304 - controlled path from git directory
	preCommitContent, err := os.ReadFile(preCommit)
	if err != nil || !strings.Contains(string(preCommitContent), "bd (beads) pre-commit hook") {
		return false
	}

	// #nosec G304 - controlled path from git directory
	postMergeContent, err := os.ReadFile(postMerge)
	if err != nil || !strings.Contains(string(postMergeContent), "bd (beads) post-merge hook") {
		return false
	}

	// Verify hooks are executable
	preCommitInfo, err := os.Stat(preCommit)
	if err != nil {
		return false
	}
	if preCommitInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	postMergeInfo, err := os.Stat(postMerge)
	if err != nil {
		return false
	}
	if postMergeInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	return true
}

// hookInfo contains information about an existing hook
type hookInfo struct {
	name        string
	path        string
	exists      bool
	isBdHook    bool
	isPreCommit bool
	content     string
}

// detectExistingHooks scans for existing git hooks
func detectExistingHooks() []hookInfo {
	gitDir, err := git.GetGitDir()
	if err != nil {
		return nil
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	hooks := []hookInfo{
		{name: "pre-commit", path: filepath.Join(hooksDir, "pre-commit")},
		{name: "post-merge", path: filepath.Join(hooksDir, "post-merge")},
		{name: "pre-push", path: filepath.Join(hooksDir, "pre-push")},
	}

	for i := range hooks {
		content, err := os.ReadFile(hooks[i].path)
		if err == nil {
			hooks[i].exists = true
			hooks[i].content = string(content)
			hooks[i].isBdHook = strings.Contains(hooks[i].content, "bd (beads)")
			// Only detect pre-commit framework if not a bd hook
			if !hooks[i].isBdHook {
				hooks[i].isPreCommit = strings.Contains(hooks[i].content, "pre-commit run") ||
					strings.Contains(hooks[i].content, ".pre-commit-config")
			}
		}
	}

	return hooks
}

// promptHookAction asks user what to do with existing hooks
func promptHookAction(existingHooks []hookInfo) string {
	fmt.Printf("\n%s Found existing git hooks:\n", ui.RenderWarn("⚠"))
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hookType := "custom script"
			if hook.isPreCommit {
				hookType = "pre-commit framework"
			}
			fmt.Printf("  - %s (%s)\n", hook.name, hookType)
		}
	}

	fmt.Printf("\nHow should bd proceed?\n")
	fmt.Printf("  [1] Chain with existing hooks (recommended)\n")
	fmt.Printf("  [2] Overwrite existing hooks\n")
	fmt.Printf("  [3] Skip git hooks installation\n")
	fmt.Printf("Choice [1-3]: ")

	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.TrimSpace(response)

	return response
}

// installGitHooks installs the bd shim hooks (same as `bd hooks install`),
// prompting when existing non-bd hooks are found.
func installGitHooks() error {
	embeddedHooks, err := getEmbeddedHooks()
	if err != nil {
		return err
	}

	existingHooks := detectExistingHooks()
	hasExistingHooks := false
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hasExistingHooks = true
			break
		}
	}

	chain := false
	if hasExistingHooks {
		choice := promptHookAction(existingHooks)
		switch choice {
		case "1", "":
			// installHooks renames existing non-bd hooks to .old and chains them
			chain = true
		case "2":
			// installHooks backs up existing hooks to .backup before overwriting
		case "3":
			fmt.Printf("Skipping git hooks installation.\n")
			fmt.Printf("You can install manually later with: %s\n", ui.RenderAccent("bd hooks install"))
			return nil
		default:
			return fmt.Errorf("invalid choice: %s", choice)
		}
	}

	return installHooks(embeddedHooks, false, false, chain)
}

// mergeDriverInstalled checks if bd merge driver is configured correctly
func mergeDriverInstalled() bool {
	// Check git config for merge driver
	cmd := exec.Command("git", "config", "merge.beads.driver")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return false
	}

	// Check if using old invalid placeholders (%L/%R from versions <0.24.0)
	// Git only supports %O (base), %A (current), %B (other)
	driverConfig := strings.TrimSpace(string(output))
	if strings.Contains(driverConfig, "%L") || strings.Contains(driverConfig, "%R") {
		// Stale config with invalid placeholders - needs repair
		return false
	}

	// Check if .gitattributes has the merge driver configured
	gitattributesPath := ".gitattributes"
	content, err := os.ReadFile(gitattributesPath)
	if err != nil {
		return false
	}

	// Look for beads JSONL merge attribute (either canonical or legacy filename)
	hasCanonical := strings.Contains(string(content), ".beads/issues.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	hasLegacy := strings.Contains(string(content), ".beads/beads.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	return hasCanonical || hasLegacy
}

// installMergeDriver configures git to use bd merge for JSONL files
func installMergeDriver() error {
	// Configure git merge driver
	cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %A %O %A %B")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to configure git merge driver: %w\n%s", err, output)
	}

	cmd = exec.Command("git", "config", "merge.beads.name", "bd JSONL merge driver")
	if output, err := cmd.CombinedOutput(); err != nil {
		// Non-fatal, the name is just descriptive
		fmt.Fprintf(os.Stderr, "Warning: failed to set merge driver name: %v\n%s", err, output)
	}

	// Create or update .gitattributes
	gitattributesPath := ".gitattributes"

	// Read existing .gitattributes if it exists
	var existingContent string
	content, err := os.ReadFile(gitattributesPath)
	if err == nil {
		existingContent = string(content)
	}

	// Check if beads merge driver is already configured
	// Check for either pattern (issues.jsonl is canonical, beads.jsonl is legacy)
	hasBeadsMerge := (strings.Contains(existingContent, ".beads/issues.jsonl") ||
		strings.Contains(existingContent, ".beads/beads.jsonl")) &&
		strings.Contains(existingContent, "merge=beads")

	if !hasBeadsMerge {
		// Append beads merge driver configuration (issues.jsonl is canonical)
		beadsMergeAttr := "\n# Use bd merge for beads JSONL files\n.beads/issues.jsonl merge=beads\n"

		newContent := existingContent
		if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
			newContent += "\n"
		}
		newContent += beadsMergeAttr

		// Write updated .gitattributes (0644 is standard for .gitattributes)
		// #nosec G306 - .gitattributes needs to be readable
		if err := os.WriteFile(gitattributesPath, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("failed to update .gitattributes: %w", err)
		}
	}

	return nil
}
