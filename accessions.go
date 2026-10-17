package ncbigenome

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// accessionRe accepts assembly accessions (optionally versioned) and BioProject
// accessions from the INSDC databases. It is slightly stricter than the CLI,
// which only checks the prefix and that the remainder parses as a number.
var accessionRe = regexp.MustCompile(`^(GC[AF]_[0-9]+(\.[0-9]+)?|PRJ(NA|DA|DB|EA|EB)[0-9]+)$`)

// Wire values of the dataset-report filter "assembly_version".
const (
	filterCurrent = "current"
	filterAll     = "all_assemblies"
)

type accessionSet struct {
	assemblies    []string
	bioprojects   []string
	versionFilter string
}

// parseAccessions normalises, validates and splits the input and applies the
// assembly-version policy.
func parseAccessions(in []string, version AssemblyVersion) (*accessionSet, error) {
	var cleaned []string
	for _, a := range in {
		a = strings.ToUpper(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if !accessionRe.MatchString(a) {
			return nil, fmt.Errorf("invalid or unsupported assembly accession: %s", a)
		}
		cleaned = append(cleaned, a)
	}
	if len(cleaned) == 0 {
		return nil, errors.New("no accessions given")
	}
	cleaned, filter := applyVersionPolicy(cleaned, version)
	set := &accessionSet{versionFilter: filter}
	for _, a := range cleaned {
		if strings.HasPrefix(a, "PRJ") {
			set.bioprojects = append(set.bioprojects, a)
		} else {
			set.assemblies = append(set.assemblies, a)
		}
	}
	return set, nil
}

// applyVersionPolicy mirrors AssemblyVersionFlag.UpdateForInputAccessions.
func applyVersionPolicy(accs []string, v AssemblyVersion) ([]string, string) {
	if v == VersionAll {
		return dedupe(stripVersions(accs)), filterAll
	}
	if hasVersion(accs) {
		if v == VersionCurrent {
			return dedupe(stripVersions(accs)), filterCurrent
		}
		// Default policy with an explicit version: return exactly that version.
		return dedupe(accs), filterAll
	}
	return dedupe(accs), filterCurrent
}

func hasVersion(accs []string) bool {
	for _, a := range accs {
		if strings.Contains(a, ".") {
			return true
		}
	}
	return false
}

func stripVersions(accs []string) []string {
	out := make([]string, 0, len(accs))
	for _, a := range accs {
		if i := strings.Index(a, "."); i != -1 {
			a = a[:i]
		}
		out = append(out, a)
	}
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
