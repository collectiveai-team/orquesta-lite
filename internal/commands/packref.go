package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

// resolvePackFlowRef turns `pack[@version]/flow@version` into the installed
// pack directory plus the `flow:name@version` ref to resolve inside it.
//
// The pack version and the flow version are independent numbers. Without an
// explicit pack pin the highest installed version wins, so publishing a new
// pack version no longer requires renaming every flow inside it.
func resolvePackFlowRef(projectDir, target string) (root string, flowRef string, err error) {
	packsRoot := filepath.Join(projectDir, ".orquestalite", "packs")
	packPart, flowPart, _ := strings.Cut(target, "/")
	flowName, flowVersion, ok := strings.Cut(flowPart, "@")
	if !ok || flowName == "" || flowVersion == "" {
		return "", "", fmt.Errorf("pack flow ref must be pack[@version]/flow@version")
	}
	packName, packVersion, pinned := strings.Cut(packPart, "@")
	if packName == "" || (pinned && packVersion == "") {
		return "", "", fmt.Errorf("pack flow ref must be pack[@version]/flow@version")
	}
	installed := installedPackVersions(packsRoot, packName)
	if !pinned {
		if len(installed) == 0 {
			return "", "", fmt.Errorf("installed pack %s is required for flow %s but no version of it was found under %s", packName, flowName, packsRoot)
		}
		packVersion = installed[len(installed)-1]
	}
	candidate := filepath.Join(packsRoot, packName, packVersion)
	if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
		return candidate, "flow:" + flowName + "@" + flowVersion, nil
	}
	return "", "", fmt.Errorf("installed pack %s@%s is required for flow %s but was not found under %s (installed: %s)", packName, packVersion, flowName, packsRoot, describeInstalledVersions(installed))
}

// installedPackVersions returns every installed version of packName in
// ascending semver order from the canonical `<name>/<version>` layout.
func installedPackVersions(packsRoot, packName string) []string {
	seen := map[string]bool{}
	var versions []string
	add := func(version string) {
		if version == "" || seen[version] || !flow.IsValidVersion(version) {
			return
		}
		seen[version] = true
		versions = append(versions, version)
	}
	for _, entry := range readDirNames(filepath.Join(packsRoot, packName)) {
		add(entry)
	}
	return flow.SortVersions(versions)
}

func readDirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func describeInstalledVersions(versions []string) string {
	if len(versions) == 0 {
		return "none"
	}
	return strings.Join(versions, ", ")
}
