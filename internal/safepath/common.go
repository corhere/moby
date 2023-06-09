package safepath

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

// evaluatePath evaluates symlinks in the concatenation of path and subpath.
func evaluatePath(path, subpath string) (string, string, error) {
	baseResolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", &ErrNotAccessible{Path: path, Cause: err}
		}
		return "", "", errors.Wrapf(err, "error while resolving symlinks in base directory %q", path)
	}

	combinedPath := filepath.Join(baseResolved, subpath)
	combinedResolved, err := filepath.EvalSymlinks(combinedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", &ErrNotAccessible{Path: combinedPath, Cause: err}
		}
		return "", "", errors.Wrapf(err, "error while resolving symlinks in combined path %q", combinedPath)
	}

	subpart, err := filepath.Rel(baseResolved, combinedResolved)
	if err != nil {
		return "", "", &ErrEscapesBase{Base: baseResolved, Subpath: subpath}
	}

	if !filepath.IsLocal(subpart) {
		return "", "", &ErrEscapesBase{Base: baseResolved, Subpath: subpath}
	}

	return baseResolved, subpart, nil
}

// isLocalTo reports whether subpath, using lexical analysis only, has all of these properties:
//
// - is within the subtree rooted at path
// - is not empty
// - on Windows, is not a reserved name such as "NUL"
//
// If isLocalTo(path, subpath) returns true, then
// filepath.Join(path, subpath)
// will always produce a path contained within path and
// filepath.Rel(path, filepath.Join(path, subpath))
// will always produce an unrooted path with no `..` elements.
//
// isLocalTo is a purely lexical operation. In particular, it does not account for the effect of any symbolic links that may exist in the filesystem.
//
// Both path and subpath are expected to be an absolute paths.
func isLocalTo(path, basepath string) bool {
	rel, err := filepath.Rel(basepath, path)
	if err != nil {
		return false
	}

	return filepath.IsLocal(rel)
}
