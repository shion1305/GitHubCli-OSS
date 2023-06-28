package create

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
)

// repoCreateInput is input parameters for the repoCreate method
type repoCreateInput struct {
	Name                 string
	HomepageURL          string
	Description          string
	Visibility           string
	OwnerLogin           string
	TeamSlug             string
	TemplateRepositoryID string
	HasIssuesEnabled     bool
	HasWikiEnabled       bool
	GitIgnoreTemplate    string
	LicenseTemplate      string
	IncludeAllBranches   bool
	InitReadme           bool
}

// createRepositoryInputV3 is the payload for the repo create REST API
type createRepositoryInputV3 struct {
	Name              string `json:"name"`
	HomepageURL       string `json:"homepage,omitempty"`
	Description       string `json:"description,omitempty"`
	IsPrivate         bool   `json:"private"`
	Visibility        string `json:"visibility,omitempty"`
	TeamID            uint64 `json:"team_id,omitempty"`
	HasIssuesEnabled  bool   `json:"has_issues"`
	HasWikiEnabled    bool   `json:"has_wiki"`
	GitIgnoreTemplate string `json:"gitignore_template,omitempty"`
	LicenseTemplate   string `json:"license_template,omitempty"`
	InitReadme        bool   `json:"auto_init,omitempty"`
}

// createRepositoryInput is the payload for the repo create GraphQL mutation
type createRepositoryInput struct {
	Name             string `json:"name"`
	HomepageURL      string `json:"homepageUrl,omitempty"`
	Description      string `json:"description,omitempty"`
	Visibility       string `json:"visibility"`
	OwnerID          string `json:"ownerId,omitempty"`
	TeamID           string `json:"teamId,omitempty"`
	HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	HasWikiEnabled   bool   `json:"hasWikiEnabled"`
}

// cloneTemplateRepositoryInput is the payload for creating a repo from a template using GraphQL
type cloneTemplateRepositoryInput struct {
	Name               string `json:"name"`
	Visibility         string `json:"visibility"`
	Description        string `json:"description,omitempty"`
	OwnerID            string `json:"ownerId"`
	RepositoryID       string `json:"repositoryId"`
	IncludeAllBranches bool   `json:"includeAllBranches"`
}

type editRepositoryInput struct {
	EnableIssues *bool   `json:"has_issues,omitempty"`
	EnableWiki   *bool   `json:"has_wiki,omitempty"`
	Homepage     *string `json:"homepage,omitempty"`
}

// repoCreate creates a new GitHub repository
func repoCreate(client *http.Client, hostname string, input repoCreateInput) (*api.Repository, error) {
	isOrg := false
	var ownerID string
	var teamID string
	var teamIDv3 uint64

	apiClient := api.NewClientFromHTTP(client)

	if input.TeamSlug != "" {
		team, err := resolveOrganizationTeam(apiClient, hostname, input.OwnerLogin, input.TeamSlug)
		if err != nil {
			return nil, err
		}
		teamIDv3 = team.ID
		teamID = team.NodeID
		ownerID = team.Organization.NodeID
		isOrg = true
	} else if input.OwnerLogin != "" {
		owner, err := resolveOwner(apiClient, hostname, input.OwnerLogin)
		if err != nil {
			return nil, err
		}
		ownerID = owner.NodeID
		isOrg = owner.IsOrganization()
	}

	if input.TemplateRepositoryID != "" {
		return createRepoFromTemplate(apiClient, ownerID, hostname, input)
	}

	if input.GitIgnoreTemplate != "" || input.LicenseTemplate != "" || input.InitReadme {
		inputv3 := createRepositoryInputV3{
			Name:              input.Name,
			HomepageURL:       input.HomepageURL,
			Description:       input.Description,
			IsPrivate:         strings.EqualFold(input.Visibility, "PRIVATE"),
			TeamID:            teamIDv3,
			HasIssuesEnabled:  input.HasIssuesEnabled,
			HasWikiEnabled:    input.HasWikiEnabled,
			GitIgnoreTemplate: input.GitIgnoreTemplate,
			LicenseTemplate:   input.LicenseTemplate,
			InitReadme:        input.InitReadme,
		}

		path := "user/repos"
		if isOrg {
			path = fmt.Sprintf("orgs/%s/repos", input.OwnerLogin)
			inputv3.Visibility = strings.ToLower(input.Visibility)
		}

		body := &bytes.Buffer{}
		enc := json.NewEncoder(body)
		if err := enc.Encode(inputv3); err != nil {
			return nil, err
		}

		repo, err := api.CreateRepoTransformToV4(apiClient, hostname, "POST", path, body)
		if err != nil {
			return nil, err
		}
		return repo, nil
	}

	var response struct {
		CreateRepository struct {
			Repository api.Repository
		}
	}

	variables := map[string]interface{}{
		"input": createRepositoryInput{
			Name:             input.Name,
			Description:      input.Description,
			HomepageURL:      input.HomepageURL,
			Visibility:       strings.ToUpper(input.Visibility),
			OwnerID:          ownerID,
			TeamID:           teamID,
			HasIssuesEnabled: input.HasIssuesEnabled,
			HasWikiEnabled:   input.HasWikiEnabled,
		},
	}

	err := apiClient.GraphQL(hostname, `
	mutation RepositoryCreate($input: CreateRepositoryInput!) {
		createRepository(input: $input) {
			repository {
				id
				name
				owner { login }
				url
			}
		}
	}
	`, variables, &response)
	if err != nil {
		return nil, err
	}

	return api.InitRepoHostname(&response.CreateRepository.Repository, hostname), nil
}

func createRepoFromTemplate(
	apiClient *api.Client,
	ownerID, hostname string,
	input repoCreateInput,
) (*api.Repository, error) {
	var response struct {
		CloneTemplateRepository struct {
			Repository api.Repository
		}
	}

	if ownerID == "" {
		var err error
		ownerID, err = api.CurrentUserID(apiClient, hostname)
		if err != nil {
			return nil, err
		}
	}

	variables := map[string]interface{}{
		"input": cloneTemplateRepositoryInput{
			Name:               input.Name,
			Description:        input.Description,
			Visibility:         strings.ToUpper(input.Visibility),
			OwnerID:            ownerID,
			RepositoryID:       input.TemplateRepositoryID,
			IncludeAllBranches: input.IncludeAllBranches,
		},
	}

	err := apiClient.GraphQL(hostname, `
		mutation CloneTemplateRepository($input: CloneTemplateRepositoryInput!) {
			cloneTemplateRepository(input: $input) {
				repository {
					id
					name
					owner { login }
					url
				}
			}
		}
		`, variables, &response)
	if err != nil {
		return nil, err
	}

	if input.HasWikiEnabled && input.HasIssuesEnabled && input.HomepageURL == "" {
		return api.InitRepoHostname(&response.CloneTemplateRepository.Repository, hostname), nil
	}

	// When repository is created with template,
	// wiki is enabled by default regardless of original template.
	// if HasWikiEnabled is false, disable wiki.
	var editRepoInput editRepositoryInput
	if !input.HasWikiEnabled {
		editRepoInput.EnableWiki = &input.HasWikiEnabled
	}
	if !input.HasIssuesEnabled {
		editRepoInput.EnableIssues = &input.HasIssuesEnabled
	}
	if input.HomepageURL != "" {
		editRepoInput.Homepage = &input.HomepageURL
	}
	req, err := json.Marshal(editRepoInput)
	if err != nil {
		return api.InitRepoHostname(&response.CloneTemplateRepository.Repository, hostname),
			fmt.Errorf("repository created, but failed to update repository settings: %w", err)
	}
	reqR := bytes.NewReader(req)
	if err := apiClient.REST(
		hostname, "PATCH", fmt.Sprintf("repos/%s/%s", ownerID, input.Name), reqR, nil,
	); err != nil {
		return nil, err
	}
	return api.InitRepoHostname(&response.CloneTemplateRepository.Repository, hostname), nil
}

type ownerResponse struct {
	NodeID string `json:"node_id"`
	Type   string `json:"type"`
}

func (r *ownerResponse) IsOrganization() bool {
	return r.Type == "Organization"
}

func resolveOwner(client *api.Client, hostname, orgName string) (*ownerResponse, error) {
	var response ownerResponse
	err := client.REST(hostname, "GET", fmt.Sprintf("users/%s", orgName), nil, &response)
	return &response, err
}

type teamResponse struct {
	ID           uint64 `json:"id"`
	NodeID       string `json:"node_id"`
	Organization struct {
		NodeID string `json:"node_id"`
	}
}

func resolveOrganizationTeam(client *api.Client, hostname, orgName, teamSlug string) (*teamResponse, error) {
	var response teamResponse
	err := client.REST(hostname, "GET", fmt.Sprintf("orgs/%s/teams/%s", orgName, teamSlug), nil, &response)
	return &response, err
}

// listGitIgnoreTemplates uses API v3 here because gitignore template isn't supported by GraphQL yet.
func listGitIgnoreTemplates(httpClient *http.Client, hostname string) ([]string, error) {
	var gitIgnoreTemplates []string
	client := api.NewClientFromHTTP(httpClient)
	err := client.REST(hostname, "GET", "gitignore/templates", nil, &gitIgnoreTemplates)
	if err != nil {
		return []string{}, err
	}
	return gitIgnoreTemplates, nil
}

// listLicenseTemplates uses API v3 here because license template isn't supported by GraphQL yet.
func listLicenseTemplates(httpClient *http.Client, hostname string) ([]api.License, error) {
	var licenseTemplates []api.License
	client := api.NewClientFromHTTP(httpClient)
	err := client.REST(hostname, "GET", "licenses", nil, &licenseTemplates)
	if err != nil {
		return nil, err
	}
	return licenseTemplates, nil
}

// Returns the current username and any orgs that user is a member of.
func userAndOrgs(httpClient *http.Client, hostname string) (string, []string, error) {
	client := api.NewClientFromHTTP(httpClient)
	return api.CurrentLoginNameAndOrgs(client, hostname)
}
