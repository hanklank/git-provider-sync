// SPDX-FileCopyrightText: 2024 Josef Andersson
//
// SPDX-License-Identifier: EUPL-1.2

// Package gitlab provides a client for interacting with GitLab repositories
// using the go-git-providers library. It offers a range of functionalities including:
//   - Creating and listing repositories
//   - Filtering repository metadata based on various criteria
//   - Validating repository names according to GitLab's rules
//   - Performing common operations on repositories
//
// This package aims to simplify GitLab interactions in Go applications, providing
// a interface for repository management and metadata handling.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"itiquette/git-provider-sync/internal/log"
	"itiquette/git-provider-sync/internal/model"
	config "itiquette/git-provider-sync/internal/model/configuration"
	"itiquette/git-provider-sync/internal/provider/targetfilter"

	"github.com/xanzy/go-gitlab"
)

// Client represents a GitLab client.
type Client struct {
	rawClient *gitlab.Client
	filter    Filter
}

// Create creates a new repository in GitLab.
func (c Client) Create(ctx context.Context, cfg config.ProviderConfig, opt model.CreateOption) error {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:Create")
	opt.DebugLog(logger).Msg("GitLab:CreateOption")

	namespaceID, err := c.getNamespaceID(ctx, cfg)
	if err != nil {
		return fmt.Errorf("get namespace ID: %w", err)
	}

	projectOpts := &gitlab.CreateProjectOptions{
		Name:          gitlab.Ptr(opt.RepositoryName),
		Description:   gitlab.Ptr(opt.Description),
		DefaultBranch: gitlab.Ptr(opt.DefaultBranch),
		Visibility:    gitlab.Ptr(toVisibility(opt.Visibility)),
	}

	if namespaceID != 0 {
		projectOpts.NamespaceID = gitlab.Ptr(namespaceID)
	}

	projectOpts.BuildsAccessLevel = gitlab.Ptr(gitlab.DisabledAccessControl)
	if opt.CIEnabled {
		projectOpts.BuildsAccessLevel = gitlab.Ptr(gitlab.PrivateAccessControl)
	}

	_, _, err = c.rawClient.Projects.CreateProject(projectOpts)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", opt.RepositoryName, err)
	}

	logger.Debug().Msg("Repository created successfully")

	return nil
}

func (c Client) DefaultBranch(ctx context.Context, owner, projectName, branch string) error {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:DefaultBranch")
	logger.Debug().Str("branch", branch).Msg("GitLab:DefaultBranch")

	_, _, err := c.rawClient.Projects.EditProject(owner+"/"+projectName, &gitlab.EditProjectOptions{
		DefaultBranch: gitlab.Ptr(branch),
	})
	if err != nil {
		return fmt.Errorf("edit project default branch: %w", err)
	}

	return nil
}

// Name returns the name of the client.
func (c Client) Name() string {
	return config.GITLAB
}

// ProjectInfos retrieves metadata information for repositories.
func (c Client) ProjectInfos(ctx context.Context, cfg config.ProviderConfig, filtering bool) ([]model.ProjectInfo, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:ProjectInfos")

	projectinfos, err := c.getRepositoryProjectInfos(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("get repository projectinfos: %w", err)
	}

	if filtering {
		return c.filter.FilterProjectinfos(ctx, cfg, projectinfos, targetfilter.FilterIncludedExcludedGen(), targetfilter.IsInInterval)
	}

	return projectinfos, nil
}

func (c Client) getRepositoryProjectInfos(ctx context.Context, cfg config.ProviderConfig) ([]model.ProjectInfo, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:getRepositoryProjectInfos")

	var allRepositories []*gitlab.Project

	if cfg.IsGroup() {
		opt := &gitlab.ListGroupProjectsOptions{
			OrderBy:     gitlab.Ptr("name"),
			Sort:        gitlab.Ptr("asc"),
			ListOptions: gitlab.ListOptions{PerPage: 100}, //TODO: add archived support, consider projectinfo struct
		}

		for {
			repositories, resp, err := c.rawClient.Groups.ListGroupProjects(cfg.Group, opt)
			if err != nil {
				return nil, fmt.Errorf("fetch group repositories page %d: %w", opt.Page, err)
			}

			allRepositories = append(allRepositories, repositories...)

			if resp.CurrentPage >= resp.TotalPages {
				break
			}

			opt.Page = resp.NextPage
		}
	} else {
		opt := &gitlab.ListProjectsOptions{
			Owned:       gitlab.Ptr(true),
			OrderBy:     gitlab.Ptr("name"),
			Sort:        gitlab.Ptr("asc"),
			ListOptions: gitlab.ListOptions{PerPage: 100},
		}

		for {
			repositories, resp, err := c.rawClient.Projects.ListUserProjects(cfg.User, opt)
			if err != nil {
				return nil, fmt.Errorf("fetch user repositories page %d: %w", opt.Page, err)
			}

			allRepositories = append(allRepositories, repositories...)

			if resp.CurrentPage >= resp.TotalPages {
				break
			}

			opt.Page = resp.NextPage
		}
	}

	logger.Debug().Int("total_repositories", len(allRepositories)).Msg("Found repositories")

	projectinfos := make([]model.ProjectInfo, 0, len(allRepositories))

	for _, repo := range allRepositories {
		if !cfg.Git.IncludeForks && repo.ForkedFromProject != nil {
			continue
		}

		rm, err := newProjectInfo(ctx, cfg, c.rawClient, repo.Path)
		if err != nil {
			return nil, fmt.Errorf("init repository meta for %s: %w", repo.Path, err)
		}

		projectinfos = append(projectinfos, rm)
	}

	return projectinfos, nil
}

// IsValidRepositoryName checks if the given name is a valid GitLab repository name.
func (c Client) IsValidRepositoryName(ctx context.Context, name string) bool {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:IsValidRepositoryName")
	logger.Debug().Str("name", name).Msg("IsValidRepositoryName")

	if !IsValidGitLabRepositoryName(name) || !isValidGitLabRepositoryNameCharacters(name) {
		logger.Debug().Str("name", name).Msg("Invalid GitLab repository name")
		logger.Debug().Msg("See https://docs.gitlab.com/ee/user/reserved_names.html")

		return false
	}

	return true
}

func newProjectInfo(ctx context.Context, cfg config.ProviderConfig, gitClient *gitlab.Client, name string) (model.ProjectInfo, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:newProjectInfo")
	logger.Debug().Str("usr/grp", cfg.User+cfg.Group).Str("name", name).Str("provider", cfg.ProviderType).Str("domain", cfg.GetDomain()).Msg("newProjectInfo")

	projectPath := getProjectPath(cfg, name)

	gitlabProject, _, err := gitClient.Projects.GetProject(projectPath, nil)
	if err != nil {
		if strings.Contains(err.Error(), "404 Not Found") {
			logger.Warn().Str("name", name).Msg("Repository not found. Ignoring.")

			return model.ProjectInfo{}, nil
		}

		return model.ProjectInfo{}, fmt.Errorf("get gitlab project: %w", err)
	}

	return model.ProjectInfo{
		OriginalName:   name,
		Description:    gitlabProject.Description,
		HTTPSURL:       gitlabProject.HTTPURLToRepo,
		SSHURL:         gitlabProject.SSHURLToRepo,
		DefaultBranch:  gitlabProject.DefaultBranch,
		LastActivityAt: gitlabProject.LastActivityAt,
		Visibility:     getVisibility(gitlabProject.Visibility),
	}, nil
}

func getVisibility(vis gitlab.VisibilityValue) string {
	switch vis {
	case gitlab.PublicVisibility:
		return "public"
	case gitlab.PrivateVisibility:
		return "private"
	case gitlab.InternalVisibility:
		return "internal"
	default:
		return "public"
	}
}

func toVisibility(vis string) gitlab.VisibilityValue {
	switch vis {
	case "private":
		return gitlab.PrivateVisibility
	case "internal":
		return gitlab.InternalVisibility
	default:
		return gitlab.PublicVisibility
	}
}

func (c Client) getNamespaceID(ctx context.Context, cfg config.ProviderConfig) (int, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:getNamespaceID")

	if !cfg.IsGroup() {
		return 0, nil
	}

	groups, resp, err := c.rawClient.Groups.ListGroups(&gitlab.ListGroupsOptions{
		Search: gitlab.Ptr(cfg.Group),
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return 0, errors.New("authentication failed: please check your token permissions")
		}

		return 0, fmt.Errorf("search for group: %w", err)
	}

	if len(groups) == 0 {
		return 0, fmt.Errorf("no group found with name: %s", cfg.Group)
	}

	return groups[0].ID, nil
}

func getProjectPath(cfg config.ProviderConfig, name string) string {
	if cfg.IsGroup() {
		return cfg.Group + "/" + name
	}

	return cfg.User + "/" + name
}

// NewGitLabClient creates a new GitLab client.
func NewGitLabClient(ctx context.Context, option model.GitProviderClientOption, httpClient *http.Client) (Client, error) {
	logger := log.Logger(ctx)
	logger.Trace().Msg("Entering GitLab:NewGitLabClient")

	defaultBaseURL := "https://gitlab.com/"

	if option.Domain != "" {
		defaultBaseURL = option.DomainWithScheme(option.HTTPClient.Scheme)
	}

	client, err := gitlab.NewClient(option.HTTPClient.Token,
		gitlab.WithBaseURL(defaultBaseURL),
		gitlab.WithHTTPClient(httpClient),
	)

	if err != nil {
		return Client{}, fmt.Errorf("create new GitLab client: %w", err)
	}

	return Client{rawClient: client}, nil
}
