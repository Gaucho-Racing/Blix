package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gaucho-racing/blix/knowledge/config"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

type Wiki struct {
	mutex      sync.RWMutex
	repository *git.Repository
	url        string
	branch     string
	path       string
	auth       transport.AuthMethod
	lastError  error
}

type State struct {
	URL       string `json:"url"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	HeadSHA   string `json:"head_sha"`
	Empty     bool   `json:"empty"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

func OpenWiki(ctx context.Context, configuration config.Config) (*Wiki, error) {
	wiki := &Wiki{
		url:    configuration.WikiRepositoryURL,
		branch: configuration.WikiRepositoryBranch,
		path:   configuration.WikiRepositoryPath,
	}
	if configuration.WikiRepositoryToken != "" {
		wiki.auth = &githttp.BasicAuth{Username: "x-access-token", Password: configuration.WikiRepositoryToken}
	}
	if err := wiki.openOrClone(ctx); err != nil {
		wiki.lastError = fmt.Errorf("open wiki repository: %w", err)
		return wiki, wiki.lastError
	}
	return wiki, nil
}

func (w *Wiki) State() (State, error) {
	w.mutex.RLock()
	defer w.mutex.RUnlock()
	state := State{
		URL:       w.url,
		Branch:    w.branch,
		Path:      w.path,
		Empty:     true,
		Connected: w.repository != nil && w.lastError == nil,
	}
	if w.lastError != nil {
		state.Error = w.lastError.Error()
	}
	if w.repository == nil {
		return state, nil
	}
	head, err := w.repository.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return state, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read repository HEAD: %w", err)
	}
	state.HeadSHA = head.Hash().String()
	state.Empty = false
	return state, nil
}

func (w *Wiki) openOrClone(ctx context.Context) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0o750); err != nil {
		return fmt.Errorf("create repository parent directory: %w", err)
	}
	repository, err := git.PlainOpen(w.path)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repository, err = git.PlainCloneContext(ctx, w.path, false, &git.CloneOptions{
			URL:           w.url,
			Auth:          w.auth,
			ReferenceName: plumbing.NewBranchReferenceName(w.branch),
			SingleBranch:  true,
		})
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			repository, err = openOrInitializeEmptyRepository(w.path, w.url, w.branch)
		}
	}
	if err != nil {
		return err
	}
	w.repository = repository
	if err := w.verifyOrigin(); err != nil {
		return err
	}
	return w.syncToRemote(ctx)
}

func openOrInitializeEmptyRepository(path string, repositoryURL string, branch string) (*git.Repository, error) {
	repository, err := git.PlainOpen(path)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repository, err = git.PlainInit(path, false)
	}
	if err != nil {
		return nil, err
	}
	if _, err := repository.Remote("origin"); errors.Is(err, git.ErrRemoteNotFound) {
		if _, err := repository.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{repositoryURL}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := repository.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD,
		plumbing.NewBranchReferenceName(branch),
	)); err != nil {
		return nil, err
	}
	return repository, nil
}

func (w *Wiki) verifyOrigin() error {
	origin, err := w.repository.Remote("origin")
	if err != nil {
		return err
	}
	configuration := origin.Config()
	if len(configuration.URLs) != 1 || configuration.URLs[0] != w.url {
		return fmt.Errorf("repository origin does not match configured URL")
	}
	return nil
}

func (w *Wiki) syncToRemote(ctx context.Context) error {
	remoteReference := plumbing.NewRemoteReferenceName("origin", w.branch)
	err := w.repository.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       w.auth,
		Force:      true,
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec("+refs/heads/" + w.branch + ":" + remoteReference.String()),
		},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return fmt.Errorf("fetch origin: %w", err)
	}
	remote, err := w.repository.Reference(remoteReference, true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	worktree, err := w.repository.Worktree()
	if err != nil {
		return err
	}
	localBranch := plumbing.NewBranchReferenceName(w.branch)
	if _, err := w.repository.Reference(localBranch, true); errors.Is(err, plumbing.ErrReferenceNotFound) {
		return worktree.Checkout(&git.CheckoutOptions{
			Branch: localBranch,
			Hash:   remote.Hash(),
			Create: true,
			Force:  true,
		})
	} else if err != nil {
		return err
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: localBranch, Force: true}); err != nil {
		return err
	}
	return worktree.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: remote.Hash()})
}
