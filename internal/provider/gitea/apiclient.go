// SPDX-FileCopyrightText: 2024 Josef Andersson
//
// SPDX-License-Identifier: EUPL-1.2

// Package gitea provides a client for interacting with Gitea repositories
// using the go-git-providers library. It offers a range of functionalities including:
//   - Creating and listing repositories
//   - Filtering repository metadata based on various criteria
//   - Validating repository names according to GitHub's rules
//   - Performing common operations on repositories
//
// This package aims to simplify Gitea interactions in Go applications, providing
// a interface for repository management and metadata handling.
package gitea

import (
	"context"
	"fmt"
	"net/http"

	"itiquette/git-provider-sync/internal/log"
	"itiquette/git-provider-sync/internal/model"
	config "itiquette/git-provider-sync/internal/model/configuration"

	"code.gitea.io/sdk/gitea"
)

// APIClient represents a Gitea client that can perform various operations
// on Gitea repositories.
type APIClient struct {
	raw           *gitea.Client
	filterService FilterService
}

// Create creates a new repository in Gitea.
// It supports creating repositories for both users and organizations.
//
// Parameters:
// - ctx: The context for the operation.
// - config: Configuration for the provider, including domain and user/group information.
// - option: Options for creating the repository, including name, visibility, and description.
//
// Returns an error if the creation fails.
func (c APIClient) Create(ctx context.Context, _ config.ProviderConfig, option model.CreateOption) (string, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Gitea:Create")
	option.DebugLog(logger).Msg("Gitea:CreateOption")

	_, _, err := c.raw.CreateRepo(gitea.CreateRepoOption{
		Name:          option.RepositoryName,
		Description:   option.Description,
		DefaultBranch: option.DefaultBranch,
	})

	if err != nil {
		return "", fmt.Errorf("failed to create repository %s: %w", option.RepositoryName, err)
	}

	logger.Trace().Msg("Repository created successfully")

	return "", nil
}

func (c APIClient) DefaultBranch(_ context.Context, _ string, _ string, _ string) error {
	return nil
}

// Name returns the name of the provider, which is "GITEA".
func (c APIClient) Name() string {
	return config.GITEA
}

// ProjectInfos retrieves metadata information for repositories.
// It can list repositories for both users and organizations.
//
// Parameters:
// - ctx: The context for the operation.
// - config: Configuration for the provider, including domain and user/group information.
// - filtering: If true, applies additional filtering to the results.
//
// Returns a slice of RepositoryMetainfo and an error if the operation fails.
func (c APIClient) ProjectInfos(ctx context.Context, config config.ProviderConfig, filtering bool) ([]model.ProjectInfo, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Gitea:Projectinfos")

	var (
		repositories []*gitea.Repository
		err          error
	)

	if config.IsGroup() {
		opt := gitea.ListOrgReposOptions{
			ListOptions: gitea.ListOptions{
				Page:     -1, // Set to -1 to get all items
				PageSize: -1,
			},
		}
		repositories, _, err = c.raw.ListOrgRepos(config.Group, opt)
	} else {
		opt := gitea.ListReposOptions{
			ListOptions: gitea.ListOptions{
				Page:     -1,
				PageSize: -1,
			},
		}

		repositories, _, err = c.raw.ListUserRepos(config.User, opt)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to fetch repositories: %w", err)
	}

	logger.Debug().Int("total_repositories", len(repositories)).Msg("Found repositories")

	var projectinfos []model.ProjectInfo //nolint:prealloc

	for _, repo := range repositories {
		if !config.Git.IncludeForks && repo.Fork {
			continue
		}

		rm, _ := newProjectInfo(ctx, config, c.raw, repo.Name)
		projectinfos = append(projectinfos, rm)
	}

	if filtering {
		return c.filterService.FilterProjectinfos(ctx, config, projectinfos)
	}

	return projectinfos, nil
}

// IsValidRepositoryName checks if the given repository name is valid for Gitea.
// It applies Gitea-specific naming rules.
//
// Parameters:
// - ctx: The context for the operation.
// - name: The repository name to validate.
//
// Returns true if the name is valid, false otherwise.
func (c APIClient) IsValidRepositoryName(ctx context.Context, name string) bool {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Gitea:IsValidRepositoryName")
	logger.Debug().Str("name", name).Msg("Gitea:Validate")

	return IsValidGiteaRepositoryName(name)
}

func (c APIClient) Protect(_ context.Context, _, _, _ string) error {
	return nil
}

func (APIClient) Unprotect(_ context.Context, _, _ string) error {
	return nil
}

// NewGiteaAPIClient creates a new Gitea client.
//
// Parameters:
// - ctx: The context for the operation.
// - option: Options for creating the client, including domain and authentication token.
//
// Returns a new Client and an error if the creation fails.
func NewGiteaAPIClient(ctx context.Context, option model.GitProviderClientOption, httpClient *http.Client) (APIClient, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Gitea:NewGiteaClient")

	clientOptions := []gitea.ClientOption{
		gitea.SetToken(option.HTTPClient.Token),
	}

	clientOptions = append(clientOptions, gitea.SetHTTPClient(httpClient))

	defaultBaseURL := "https://gitea.com"

	if option.Domain != "" {
		defaultBaseURL = option.DomainWithScheme(option.HTTPClient.Scheme)
	}

	client, err := gitea.NewClient(
		defaultBaseURL,
		clientOptions...,
	)
	if err != nil {
		return APIClient{}, fmt.Errorf("failed to create a new Gitea client: %w", err)
	}

	return APIClient{raw: client}, nil
}

// newProjectInfo creates a new RepositoryMetainfo struct for a given repository.
// It fetches detailed information about the repository from Gitea.
//
// Parameters:
// - ctx: The context for the operation.
// - config: Configuration for the provider.
// - gitClient: The Gitea client to use for fetching repository information.
// - repositoryName: The name of the repository to fetch information for.
//
// Returns a RepositoryMetainfo and an error if the operation fails.
func newProjectInfo(ctx context.Context, config config.ProviderConfig, rawClient *gitea.Client, repositoryName string) (model.ProjectInfo, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering Gitea:newProjectInfo")

	owner := config.Group
	if !config.IsGroup() {
		owner = config.User
	}

	giteaProject, _, err := rawClient.GetRepo(owner, repositoryName)
	if err != nil {
		return model.ProjectInfo{}, fmt.Errorf("failed to get project info for %s: %w", repositoryName, err)
	}

	return model.ProjectInfo{
		OriginalName:   repositoryName,
		HTTPSURL:       giteaProject.CloneURL,
		SSHURL:         giteaProject.SSHURL,
		Description:    giteaProject.Description,
		DefaultBranch:  giteaProject.DefaultBranch,
		LastActivityAt: &giteaProject.Updated,
		Visibility:     string(giteaProject.Owner.Visibility),
	}, nil
}

// TODO: Implement isValidGiteaRepositoryName and isValidGiteaRepositoryNameCharacters functions
// These functions should contain the logic for validating Gitea repository names
// according to Gitea's specific naming rules and allowed characters.