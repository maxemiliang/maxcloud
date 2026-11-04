// Package kustomize edits kustomization.yaml files as text, so their
// comments survive.
package kustomize

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var resourceItem = regexp.MustCompile(`^(\s+)-\s+(\S+)\s*$`)

// resourcesBlock finds the items of the top-level `resources:` list: their
// line indexes, values and indentation. Edits are made on the text so the
// file's comments survive.
func resourcesBlock(lines []string) (start int, items []int, indent string, err error) {
	start = -1
	for i, l := range lines {
		if strings.TrimRight(l, " ") == "resources:" {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, nil, "", fmt.Errorf("kustomization.yaml has no top-level resources: list")
	}
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		m := resourceItem.FindStringSubmatch(l)
		if m == nil {
			break
		}
		items = append(items, i)
		indent = m[1]
	}
	if indent == "" {
		indent = "  "
	}
	return start, items, indent, nil
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") + "\n" }

// AddResource lists res in the kustomization, after the other entries of the
// same directory and in sorted order. It is a no-op if res is already listed.
func AddResource(kustomization, res string) (string, bool, error) {
	lines := splitLines(kustomization)
	start, items, indent, err := resourcesBlock(lines)
	if err != nil {
		return "", false, err
	}
	var values []string
	for _, i := range items {
		v := resourceItem.FindStringSubmatch(lines[i])[2]
		if v == res {
			return kustomization, false, nil
		}
		values = append(values, v)
	}
	// Insert before the first entry of the same directory that sorts after
	// res, else after the directory's last entry, else at the end.
	dir := res[:strings.LastIndex(res, "/")+1]
	at := -1
	for n, v := range values {
		if !strings.HasPrefix(v, dir) {
			continue
		}
		if v > res {
			at = items[n]
			break
		}
		at = items[n] + 1
	}
	if at < 0 {
		at = start + 1
		if len(items) > 0 {
			at = items[len(items)-1] + 1
		}
	}
	line := indent + "- " + res
	lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	return joinLines(lines), true, nil
}

// RemoveResource drops res from the kustomization. It is a no-op if res isn't
// listed.
func RemoveResource(kustomization, res string) (string, bool, error) {
	lines := splitLines(kustomization)
	_, items, _, err := resourcesBlock(lines)
	if err != nil {
		return "", false, err
	}
	for _, i := range items {
		if resourceItem.FindStringSubmatch(lines[i])[2] == res {
			lines = append(lines[:i], lines[i+1:]...)
			return joinLines(lines), true, nil
		}
	}
	return kustomization, false, nil
}

// Resources lists the kustomization's resources, sorted.
func Resources(kustomization string) ([]string, error) {
	lines := splitLines(kustomization)
	_, items, _, err := resourcesBlock(lines)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, i := range items {
		out = append(out, resourceItem.FindStringSubmatch(lines[i])[2])
	}
	sort.Strings(out)
	return out, nil
}
