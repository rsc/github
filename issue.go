// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package github

import (
	"fmt"
	"time"

	"rsc.io/github/schema"
)

const issueFields = `
  number
  title
  id
  author { __typename login }
  closed
  closedAt
  createdAt
  lastEditedAt
  milestone { id number title }
  repository { name owner { __typename login } }
  body
  url
  labels(first: 100) {
    nodes {
      name
      description
      id
      repository { name owner { __typename login } }
    }
  }
`

func (c *Client) Issue(org, repo string, n int) (*Issue, error) {
	graphql := `
	  query($Org: String!, $Repo: String!, $Number: Int!) {
	    organization(login: $Org) {
	      repository(name: $Repo) {
	        issue(number: $Number) {
	          ` + issueFields + `
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": org, "Repo": repo, "Number": n}
	q, err := c.GraphQLQuery(graphql, vars)
	if err != nil {
		return nil, err
	}
	issue := toIssue(q.Organization.Repository.Issue)
	return issue, nil
}

func (c *Client) SearchLabels(org, repo, query string) ([]*Label, error) {
	graphql := `
	  query($Org: String!, $Repo: String!, $Query: String, $Cursor: String) {
	    repository(owner: $Org, name: $Repo) {
	      labels(first: 100, query: $Query, after: $Cursor) {
	        pageInfo {
	          hasNextPage
	          endCursor
	        }
	        totalCount
	        nodes {
	          name
	          description
	          id
	          repository { name owner { __typename login } }
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": org, "Repo": repo}
	if query != "" {
		vars["Query"] = query
	}
	return collect(c, graphql, vars, toLabel,
		func(q *schema.Query) pager[*schema.Label] { return q.Repository.Labels },
	)
}

func (c *Client) Discussions(org, repo string) ([]*Discussion, error) {
	graphql := `
	  query($Org: String!, $Repo: String!, $Cursor: String) {
	    repository(owner: $Org, name: $Repo) {
	      discussions(first: 100, after: $Cursor) {
	        pageInfo {
	          hasNextPage
	          endCursor
	        }
	        totalCount
	        nodes {
	          locked
	          closed
	          closedAt
	          number
	          title
	          repository { name owner { __typename login } }
	          body
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": org, "Repo": repo}
	return collect(c, graphql, vars, toDiscussion,
		func(q *schema.Query) pager[*schema.Discussion] { return q.Repository.Discussions },
	)
}

func (c *Client) SearchMilestones(org, repo, query string) ([]*Milestone, error) {
	graphql := `
	  query($Org: String!, $Repo: String!, $Query: String, $Cursor: String) {
	    repository(owner: $Org, name: $Repo) {
	      milestones(first: 100, query: $Query, after: $Cursor) {
	        pageInfo {
	          hasNextPage
	          endCursor
	        }
	        totalCount
	        nodes {
	          id
	          number
	          title
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": org, "Repo": repo}
	if query != "" {
		vars["Query"] = query
	}
	return collect(c, graphql, vars, toMilestone,
		func(q *schema.Query) pager[*schema.Milestone] { return q.Repository.Milestones },
	)
}

func (c *Client) IssueComments(issue *Issue) ([]*IssueComment, error) {
	graphql := `
	  query($Org: String!, $Repo: String!, $Number: Int!, $Cursor: String) {
	    repository(owner: $Org, name: $Repo) {
	      issue(number: $Number) {
	        comments(first: 100, after: $Cursor) {
	          pageInfo {
	            hasNextPage
	            endCursor
	          }
	          totalCount
	          nodes {
	            author { __typename login }
	            id
	            body
	            createdAt
	            publishedAt
	            updatedAt
	            issue { number }
	            repository { name owner { __typename login } }
	          }
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": issue.Owner, "Repo": issue.Repo, "Number": issue.Number}
	return collect(c, graphql, vars, toIssueComment,
		func(q *schema.Query) pager[*schema.IssueComment] { return q.Repository.Issue.Comments },
	)
}

func (c *Client) UserComments(user string) ([]*IssueComment, error) {
	graphql := `
	  query($User: String!, $Cursor: String) {
	    user(login: $User) {
	      issueComments(first: 100, after: $Cursor) {
	        pageInfo {
	          hasNextPage
	          endCursor
	        }
	        totalCount
	        nodes {
	          author { __typename login }
	          id
	          body
	          createdAt
	          publishedAt
	          updatedAt
	          issue { number }
	          repository { name owner { __typename login } }
	        }
	      }
	    }
	  }
	`

	vars := Vars{"User": user}
	return collect(c, graphql, vars, toIssueComment,
		func(q *schema.Query) pager[*schema.IssueComment] { return q.User.IssueComments },
	)
}

func (c *Client) AddIssueComment(issue *Issue, text string) error {
	graphql := `
	  mutation($ID: ID!, $Text: String!) {
	    addComment(input: {subjectId: $ID, body: $Text}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"ID": issue.ID, "Text": text})
	return err
}

func (c *Client) CloseIssue(issue *Issue) error {
	graphql := `
	  mutation($ID: ID!) {
	    closeIssue(input: {issueId: $ID}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"ID": issue.ID})
	return err
}

func (c *Client) ReopenIssue(issue *Issue) error {
	graphql := `
	  mutation($ID: ID!) {
	    reopenIssue(input: {issueId: $ID}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"ID": issue.ID})
	return err
}

func (c *Client) AddIssueLabels(issue *Issue, labels ...*Label) error {
	var labelIDs []string
	for _, lab := range labels {
		labelIDs = append(labelIDs, lab.ID)
	}
	graphql := `
	  mutation($Issue: ID!, $Labels: [ID!]!) {
	    addLabelsToLabelable(input: {labelableId: $Issue, labelIds: $Labels}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Issue": issue.ID, "Labels": labelIDs})
	return err
}

func (c *Client) RemoveIssueLabels(issue *Issue, labels ...*Label) error {
	var labelIDs []string
	for _, lab := range labels {
		labelIDs = append(labelIDs, lab.ID)
	}
	graphql := `
	  mutation($Issue: ID!, $Labels: [ID!]!) {
	    removeLabelsFromLabelable(input: {labelableId: $Issue, labelIds: $Labels}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Issue": issue.ID, "Labels": labelIDs})
	return err
}

func (c *Client) CreateIssue(repo *Repo, title, body string, extra ...any) (*Issue, error) {
	var labelIDs []string
	var projectIDs []string
	for _, x := range extra {
		switch x := x.(type) {
		default:
			return nil, fmt.Errorf("cannot create issue with extra of type %T", x)
		case *Label:
			labelIDs = append(labelIDs, x.ID)
		case *Project:
			projectIDs = append(projectIDs, x.ID)
		}
	}
	graphql := `
	  mutation($Repo: ID!, $Title: String!, $Body: String!, $Labels: [ID!]!) {
	    createIssue(input: {repositoryId: $Repo, title: $Title, body: $Body, labelIds: $Labels}) {
	      clientMutationId
	      issue {
	      ` + issueFields + `
	      }
	    }
	  }
	`
	m, err := c.GraphQLMutation(graphql, Vars{"Repo": repo.ID, "Title": title, "Body": body, "Labels": labelIDs, "Projects": projectIDs})
	if err != nil {
		return nil, err
	}
	issue := toIssue(m.CreateIssue.Issue)
	for _, id := range projectIDs {
		graphql := `
		  mutation($Project: ID!, $Issue: ID!) {
		    addProjectV2ItemById(input: {projectId: $Project, contentId: $Issue}) {
		      clientMutationId
		    }
		  }
		`
		_, err := c.GraphQLMutation(graphql, Vars{"Project": id, "Issue": string(m.CreateIssue.Issue.Id)})
		if err != nil {
			return issue, err
		}
	}
	return issue, nil
}

func (c *Client) RetitleIssue(issue *Issue, title string) error {
	graphql := `
	  mutation($Issue: ID!, $Title: String!) {
	    updateIssue(input: {id: $Issue, title: $Title}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Issue": issue.ID, "Title": title})
	return err
}

func (c *Client) EditIssueComment(comment *IssueComment, body string) error {
	graphql := `
	  mutation($Comment: ID!, $Body: String!) {
	    updateIssueComment(input: {id: $Comment, body: $Body}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Comment": comment.ID, "Body": body})
	return err
}

func (c *Client) DeleteIssue(issue *Issue) error {
	graphql := `
	  mutation($Issue: ID!) {
	    deleteIssue(input: {issueId: $Issue}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Issue": issue.ID})
	return err
}

func (c *Client) RemilestoneIssue(issue *Issue, milestone *Milestone) error {
	graphql := `
	  mutation($Issue: ID!, $Milestone: ID!) {
	    updateIssue(input: {id: $Issue, milestoneId: $Milestone}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Issue": issue.ID, "Milestone": milestone.ID})
	return err
}

func (c *Client) SetProjectItemFieldOption(project *Project, item *ProjectItem, field *ProjectField, option *ProjectFieldOption) error {
	graphql := `
	  mutation($Project: ID!, $Item: ID!, $Field: ID!, $Option: String!) {
	    updateProjectV2ItemFieldValue(input: {projectId: $Project, itemId: $Item, fieldId: $Field, value: {singleSelectOptionId: $Option}}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Project": project.ID, "Item": item.ID, "Field": field.ID, "Option": option.ID})
	return err
}

func (c *Client) DeleteProjectItem(project *Project, item *ProjectItem) error {
	graphql := `
	  mutation($Project: ID!, $Item: ID!) {
	    deleteProjectV2Item(input: {projectId: $Project, itemId: $Item}) {
	      clientMutationId
	    }
	  }
	`
	_, err := c.GraphQLMutation(graphql, Vars{"Project": project.ID, "Item": item.ID})
	return err
}

type Label struct {
	Name        string
	Description string
	ID          string
	Owner       string
	Repo        string
}

func toLabel(s *schema.Label) *Label {
	return &Label{
		Name:        s.Name,
		Description: s.Description,
		ID:          string(s.Id),
		Owner:       toOwner(&s.Repository.Owner),
		Repo:        s.Repository.Name,
	}
}

type Discussion struct {
	Title    string
	Number   int
	Locked   bool
	Closed   bool
	ClosedAt time.Time
	Owner    string
	Repo     string
	Body     string
}

func toAuthor(a *schema.Actor) string {
	if a != nil && a.Interface != nil {
		return a.Interface.GetLogin()
	}
	return ""
}

func toOwner(o *schema.RepositoryOwner) string {
	if o != nil && o.Interface != nil {
		return o.Interface.(interface{ GetLogin() string }).GetLogin()
	}
	return ""
}

func toDiscussion(s *schema.Discussion) *Discussion {
	return &Discussion{
		Title:    s.Title,
		Number:   s.Number,
		Locked:   s.Locked,
		Closed:   s.Closed,
		ClosedAt: toTime(s.ClosedAt),
		Owner:    toOwner(&s.Repository.Owner),
		Repo:     s.Repository.Name,
		Body:     s.Body,
	}
}

type Milestone struct {
	Title string
	ID    string
}

func toMilestone(s *schema.Milestone) *Milestone {
	if s == nil {
		return nil
	}
	return &Milestone{
		Title: s.Title,
		ID:    string(s.Id),
	}
}

type Issue struct {
	ID           string
	Title        string
	Number       int
	Closed       bool
	ClosedAt     time.Time
	CreatedAt    time.Time
	LastEditedAt time.Time
	Labels       []*Label
	Milestone    *Milestone
	Author       string
	Owner        string
	Repo         string
	Body         string
	URL          string
}

func toIssue(s *schema.Issue) *Issue {
	return &Issue{
		ID:           string(s.Id),
		Title:        s.Title,
		Number:       s.Number,
		Author:       toAuthor(&s.Author),
		Closed:       s.Closed,
		ClosedAt:     toTime(s.ClosedAt),
		CreatedAt:    toTime(s.CreatedAt),
		LastEditedAt: toTime(s.LastEditedAt),
		Owner:        toOwner(&s.Repository.Owner),
		Repo:         s.Repository.Name,
		Milestone:    toMilestone(s.Milestone),
		Labels:       apply(toLabel, s.Labels.Nodes),
		Body:         s.Body,
		URL:          string(s.Url),
	}
}

func (i *Issue) LabelByName(name string) *Label {
	for _, lab := range i.Labels {
		if lab.Name == name {
			return lab
		}
	}
	return nil
}

type IssueComment struct {
	ID          string
	Author      string
	Body        string
	CreatedAt   time.Time
	PublishedAt time.Time
	UpdatedAt   time.Time
	Issue       int
	Owner       string
	Repo        string
}

func toIssueComment(s *schema.IssueComment) *IssueComment {
	return &IssueComment{
		Author:      toAuthor(&s.Author),
		Body:        s.Body,
		CreatedAt:   toTime(s.CreatedAt),
		ID:          string(s.Id),
		PublishedAt: toTime(s.PublishedAt),
		UpdatedAt:   toTime(s.UpdatedAt),
		Issue:       s.Issue.GetNumber(),
		Owner:       toOwner(&s.Repository.Owner),
		Repo:        s.Repository.Name,
	}
}

type Repo struct {
	Owner string
	Repo  string
	ID    string
}

func (c *Client) Repo(org, repo string) (*Repo, error) {
	graphql := `
	  query($Org: String!, $Repo: String!) {
	    repository(owner: $Org, name: $Repo) {
	      id
	    }
	  }
	`
	vars := Vars{"Org": org, "Repo": repo}
	q, err := c.GraphQLQuery(graphql, vars)
	if err != nil {
		return nil, err
	}
	return &Repo{org, repo, string(q.Repository.Id)}, nil
}
