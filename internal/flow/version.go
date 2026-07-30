package flow

import (
	"sort"
	"strconv"
	"strings"
)

// IsValidVersion reports whether raw is a version string this package accepts
// (`N`, `N.N`, `N.N.N`, optionally followed by one `-prerelease` or `+build`
// suffix). Callers scanning a directory of installed versions use it to skip
// entries that are not versions at all.
func IsValidVersion(raw string) bool { return versionPattern.MatchString(raw) }

// CompareVersions orders two version strings using semver precedence rules,
// returning -1, 0, or 1. Missing numeric fields count as zero, so `1`, `1.0`
// and `1.0.0` have equal precedence; numeric fields compare numerically, so
// `1.10` sorts above `1.2`; a prerelease suffix sorts *below* the same version
// without one; and build metadata (`+...`) is ignored entirely.
//
// Lexical or integer comparison is not enough: versionPattern admits all of
// the forms above, and pack selection ("the highest installed version") must
// agree with what an operator reads as higher.
func CompareVersions(a, b string) int {
	aCore, aPre := splitVersion(a)
	bCore, bPre := splitVersion(b)
	for index := 0; index < 3; index++ {
		if order := compareInts(numericField(aCore, index), numericField(bCore, index)); order != 0 {
			return order
		}
	}
	return comparePrerelease(aPre, bPre)
}

// SortVersions sorts a copy of versions in ascending precedence order; the
// highest version is the last element.
func SortVersions(versions []string) []string {
	sorted := append([]string(nil), versions...)
	sort.SliceStable(sorted, func(i, j int) bool { return CompareVersions(sorted[i], sorted[j]) < 0 })
	return sorted
}

// splitVersion separates the dotted numeric core from the prerelease suffix,
// discarding build metadata.
func splitVersion(raw string) ([]string, string) {
	if base, _, found := strings.Cut(raw, "+"); found {
		raw = base
	}
	core, prerelease, _ := strings.Cut(raw, "-")
	return strings.Split(core, "."), prerelease
}

func numericField(fields []string, index int) int {
	if index >= len(fields) {
		return 0
	}
	value, err := strconv.Atoi(fields[index])
	if err != nil {
		return 0
	}
	return value
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePrerelease implements semver's prerelease precedence: no prerelease
// outranks any prerelease; otherwise dot-separated identifiers compare
// field-by-field, numeric identifiers below alphanumeric ones, and a shorter
// run of otherwise-equal identifiers sorts lower.
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	aFields, bFields := strings.Split(a, "."), strings.Split(b, ".")
	for index := 0; index < len(aFields) && index < len(bFields); index++ {
		aValue, aNumeric := strconv.Atoi(aFields[index])
		bValue, bNumeric := strconv.Atoi(bFields[index])
		switch {
		case aNumeric == nil && bNumeric == nil:
			if order := compareInts(aValue, bValue); order != 0 {
				return order
			}
		case aNumeric == nil:
			return -1
		case bNumeric == nil:
			return 1
		default:
			if order := strings.Compare(aFields[index], bFields[index]); order != 0 {
				return order
			}
		}
	}
	return compareInts(len(aFields), len(bFields))
}
