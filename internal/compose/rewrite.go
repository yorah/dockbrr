package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LocatedImage is the result of resolving a service's image line in the raw
// config files: the file + 1-based line holding the literal, the current ref,
// and whether it is safe to rewrite in place.
type LocatedImage struct {
	File       string
	Line       int
	OldRef     string
	Rewritable bool
}

// LocateImageLine finds services.<service>.image across configFiles. The last
// file that declares it as a plain literal scalar wins (compose precedence).
// Rewritable is false (with nil error) when the image is absent or interpolated
// (contains "${"). A file that fails to parse is a hard error.
func LocateImageLine(configFiles []string, service string) (LocatedImage, error) {
	var found LocatedImage
	for _, f := range configFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			return LocatedImage{}, fmt.Errorf("read %s: %w", f, err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return LocatedImage{}, fmt.Errorf("parse %s: %w", f, err)
		}
		node := imageScalar(&doc, service)
		if node == nil {
			continue
		}
		val := node.Value
		loc := LocatedImage{File: f, Line: node.Line, OldRef: val, Rewritable: true}
		if strings.Contains(val, "${") || node.Kind == yaml.AliasNode || val == "" {
			loc.Rewritable = false
		}
		found = loc // later file overrides earlier
	}
	return found, nil
}

// imageScalar walks a parsed document for services.<service>.image and returns
// its scalar node (nil if not present as a mapping value).
func imageScalar(doc *yaml.Node, service string) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	services := mapValue(root, "services")
	if services == nil {
		return nil
	}
	svc := mapValue(services, service)
	if svc == nil {
		return nil
	}
	return mapValue(svc, "image")
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ReplaceImageLine swaps oldRef for newRef on the 1-based line only, leaving
// every other byte untouched. It errors if that line does not contain oldRef.
func ReplaceImageLine(content, oldRef, newRef string, line int) (string, error) {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range (%d lines)", line, len(lines))
	}
	i := line - 1
	if !strings.Contains(lines[i], oldRef) {
		return "", fmt.Errorf("line %d does not contain %q", line, oldRef)
	}
	lines[i] = strings.Replace(lines[i], oldRef, newRef, 1)
	return strings.Join(lines, "\n"), nil
}

// WriteFileAtomic writes content to a temp file in path's directory, then
// renames it over path, so a crash mid-write never leaves a truncated file.
// Preserves the existing file's permissions if it exists, or uses 0644 for new files.
func WriteFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dockbrr-write-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	// Preserve the existing file's permissions, or use 0644 for new files.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return err
	}

	return os.Rename(tmpName, path)
}
