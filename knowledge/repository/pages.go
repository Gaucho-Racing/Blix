package repository

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/pkg/githubapp"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var (
	ErrInvalid      = errors.New("invalid wiki request")
	ErrConflict     = errors.New("wiki revision conflict")
	ErrNotFound     = errors.New("wiki page not found")
	ErrUnavailable  = errors.New("wiki unavailable")
	pagePathPattern = regexp.MustCompile(`^[a-zA-Z0-9_/-][a-zA-Z0-9_./-]*\.md$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const MaxPageBytes = 1024 * 1024

type Page struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blob_sha"`
	Content string `json:"content"`
}

type Snapshot struct {
	CommitSHA string `json:"commit_sha"`
	Pages     []Page `json:"pages"`
}

type PageRevision struct {
	CommitSHA string `json:"commit_sha"`
	Exists    bool   `json:"exists"`
	Content   string `json:"content"`
}

func (w *Wiki) PageAt(ctx context.Context, commitSHA, pagePath string) (PageRevision, error) {
	if !ValidCommit(commitSHA) || !ValidPagePath(pagePath) {
		return PageRevision{}, ErrInvalid
	}
	result := PageRevision{CommitSHA: commitSHA}
	if commitSHA == "" {
		return result, nil
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.repository == nil {
		return result, ErrUnavailable
	}
	if err := w.syncToRemote(ctx); err != nil {
		return result, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	commit, err := w.repository.CommitObject(plumbing.NewHash(commitSHA))
	if err != nil {
		return result, fmt.Errorf("%w: base revision is unavailable", ErrUnavailable)
	}
	tree, err := commit.Tree()
	if err != nil {
		return result, err
	}
	err = tree.Files().ForEach(func(file *object.File) error {
		if file.Name != pagePath {
			return nil
		}
		if file.Size > MaxPageBytes || (file.Mode != filemode.Regular && file.Mode != filemode.Executable) {
			return ErrInvalid
		}
		content, err := file.Contents()
		if err != nil {
			return err
		}
		result.Exists, result.Content = true, content
		return nil
	})
	return result, err
}

type Edit struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

func ValidCommit(value string) bool { return value == "" || commitPattern.MatchString(value) }

func ValidPagePath(value string) bool {
	if len(value) > 512 || !pagePathPattern.MatchString(value) || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func ValidateEdits(edits []Edit) error {
	if len(edits) == 0 || len(edits) > 100 {
		return fmt.Errorf("%w: provide between 1 and 100 files", ErrInvalid)
	}
	seen := make(map[string]bool)
	total := 0
	for _, edit := range edits {
		if !ValidPagePath(edit.Path) || seen[edit.Path] {
			return fmt.Errorf("%w: invalid or duplicate Markdown path", ErrInvalid)
		}
		seen[edit.Path] = true
		if edit.Operation != "upsert" && edit.Operation != "delete" {
			return fmt.Errorf("%w: operation must be upsert or delete", ErrInvalid)
		}
		if edit.Operation == "delete" && edit.Content != "" {
			return fmt.Errorf("%w: deletes cannot contain content", ErrInvalid)
		}
		if !utf8.ValidString(edit.Content) || strings.ContainsRune(edit.Content, 0) || len(edit.Content) > MaxPageBytes {
			return fmt.Errorf("%w: content must be UTF-8 text up to 1 MiB", ErrInvalid)
		}
		total += len(edit.Content)
	}
	if total > 4*MaxPageBytes {
		return fmt.Errorf("%w: changeset exceeds 4 MiB", ErrInvalid)
	}
	return nil
}

func (w *Wiki) Refresh(ctx context.Context) (string, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.repository == nil || w.installation == nil {
		return "", ErrUnavailable
	}
	if err := w.syncToRemote(ctx); err != nil {
		w.lastError = err
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	w.lastError = nil
	return w.headSHA()
}

func (w *Wiki) headSHA() (string, error) {
	head, err := w.repository.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

func (w *Wiki) Pages(ctx context.Context, pagePath string, prefix string) (Snapshot, error) {
	if pagePath != "" && !ValidPagePath(pagePath) {
		return Snapshot{}, ErrInvalid
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.repository == nil {
		return Snapshot{}, ErrUnavailable
	}
	if err := w.syncToRemote(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	head, err := w.headSHA()
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{CommitSHA: head, Pages: []Page{}}
	if head == "" {
		if pagePath != "" {
			return Snapshot{}, ErrNotFound
		}
		return result, nil
	}
	commit, err := w.repository.CommitObject(plumbing.NewHash(head))
	if err != nil {
		return Snapshot{}, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return Snapshot{}, err
	}
	err = tree.Files().ForEach(func(file *object.File) error {
		if !ValidPagePath(file.Name) || file.Mode != filemode.Regular && file.Mode != filemode.Executable ||
			!strings.HasPrefix(file.Name, prefix) || pagePath != "" && file.Name != pagePath {
			return nil
		}
		page := Page{Path: file.Name, BlobSHA: file.Hash.String()}
		if pagePath != "" {
			if file.Size > MaxPageBytes {
				return fmt.Errorf("%w: page exceeds 1 MiB", ErrInvalid)
			}
			content, err := file.Contents()
			if err != nil {
				return err
			}
			page.Content = content
		}
		result.Pages = append(result.Pages, page)
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	if pagePath != "" && len(result.Pages) == 0 {
		return Snapshot{}, ErrNotFound
	}
	sort.Slice(result.Pages, func(i, j int) bool { return result.Pages[i].Path < result.Pages[j].Path })
	return result, nil
}

func (w *Wiki) Prepare(ctx context.Context, base string, changesetID string, summary string, edits []Edit, when time.Time) (string, error) {
	if !ValidCommit(base) {
		return "", ErrInvalid
	}
	if err := ValidateEdits(edits); err != nil {
		return "", err
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.repository == nil {
		return "", ErrUnavailable
	}
	if err := w.syncToRemote(ctx); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	head, err := w.headSHA()
	if err != nil {
		return "", err
	}
	if head != base {
		return "", fmt.Errorf("%w: read the latest pages and propose a new changeset", ErrConflict)
	}
	var treeHash plumbing.Hash
	if base != "" {
		commit, err := w.repository.CommitObject(plumbing.NewHash(base))
		if err != nil {
			return "", err
		}
		treeHash = commit.TreeHash
	}
	originalTree := treeHash
	for _, edit := range edits {
		treeHash, err = w.editTree(treeHash, strings.Split(edit.Path, "/"), edit)
		if err != nil {
			return "", err
		}
	}
	if treeHash == originalTree {
		return "", fmt.Errorf("%w: changeset makes no changes", ErrInvalid)
	}
	if w.authorName == "" {
		w.authorName, w.authorEmail, err = githubapp.BotIdentity(ctx, w.appID, w.privateKey)
		if err != nil {
			return "", err
		}
	}
	author := object.Signature{Name: w.authorName, Email: w.authorEmail, When: when.UTC()}
	commit := &object.Commit{Author: author, Committer: author, Message: summary + "\n\nChangeset-ID: " + changesetID + "\n", TreeHash: treeHash}
	if base != "" {
		commit.ParentHashes = []plumbing.Hash{plumbing.NewHash(base)}
	}
	encoded := w.repository.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		return "", err
	}
	hash, err := w.repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		return "", err
	}
	if err := w.repository.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/changesets/"+changesetID), hash)); err != nil {
		return "", err
	}
	return hash.String(), nil
}

func (w *Wiki) editTree(hash plumbing.Hash, parts []string, edit Edit) (plumbing.Hash, error) {
	tree := &object.Tree{}
	if !hash.IsZero() {
		stored, err := w.repository.TreeObject(hash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		tree.Entries = append(tree.Entries, stored.Entries...)
	}
	index := -1
	for i, entry := range tree.Entries {
		if entry.Name == parts[0] {
			index = i
			break
		}
	}
	entry := object.TreeEntry{Name: parts[0]}
	if index >= 0 {
		entry = tree.Entries[index]
	}
	remove := false
	if len(parts) > 1 {
		if index >= 0 && entry.Mode != filemode.Dir {
			return plumbing.ZeroHash, fmt.Errorf("%w: path parent is not a directory", ErrConflict)
		}
		child, err := w.editTree(entry.Hash, parts[1:], edit)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entry.Hash, entry.Mode = child, filemode.Dir
		childTree, err := w.repository.TreeObject(child)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		remove = len(childTree.Entries) == 0
	} else {
		if index >= 0 && entry.Mode != filemode.Regular && entry.Mode != filemode.Executable {
			return plumbing.ZeroHash, fmt.Errorf("%w: path is not a regular file", ErrConflict)
		}
		if edit.Operation == "delete" {
			if index < 0 {
				return plumbing.ZeroHash, ErrNotFound
			}
			remove = true
		} else {
			blob := w.repository.Storer.NewEncodedObject()
			blob.SetType(plumbing.BlobObject)
			writer, err := blob.Writer()
			if err != nil {
				return plumbing.ZeroHash, err
			}
			_, writeErr := writer.Write([]byte(edit.Content))
			closeErr := writer.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				return plumbing.ZeroHash, err
			}
			entry.Hash, err = w.repository.Storer.SetEncodedObject(blob)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			entry.Mode = filemode.Regular
		}
	}
	if index >= 0 {
		if remove {
			tree.Entries = append(tree.Entries[:index], tree.Entries[index+1:]...)
		} else {
			tree.Entries[index] = entry
		}
	} else if !remove {
		tree.Entries = append(tree.Entries, entry)
	}
	sort.Sort(object.TreeEntrySorter(tree.Entries))
	encoded := w.repository.Storer.NewEncodedObject()
	if err := tree.Encode(encoded); err != nil {
		return plumbing.ZeroHash, err
	}
	return w.repository.Storer.SetEncodedObject(encoded)
}

func (w *Wiki) Publish(ctx context.Context, base string, result string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.repository == nil {
		return ErrUnavailable
	}
	if !ValidCommit(base) || result == "" || !ValidCommit(result) {
		return ErrInvalid
	}
	if err := w.syncToRemote(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	head, err := w.headSHA()
	if err != nil {
		return err
	}
	commit, err := w.repository.CommitObject(plumbing.NewHash(result))
	if err != nil {
		return err
	}
	if head != "" {
		remoteCommit, err := w.repository.CommitObject(plumbing.NewHash(head))
		if err != nil {
			return err
		}
		published, err := commit.IsAncestor(remoteCommit)
		if err != nil {
			return err
		}
		if published {
			return nil
		}
	}
	if head != base {
		return ErrConflict
	}
	auth, err := w.authentication(ctx)
	if err != nil {
		return err
	}
	branch := plumbing.NewBranchReferenceName(w.branch)
	options := &git.PushOptions{RemoteName: "origin", Auth: auth, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(result + ":" + branch.String())}}
	if base != "" {
		options.RequireRemoteRefs = []gitconfig.RefSpec{gitconfig.RefSpec(base + ":" + branch.String())}
	}
	if err := w.repository.PushContext(ctx, options); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("push wiki changeset: %w", err)
	}
	return w.syncToRemote(ctx)
}
