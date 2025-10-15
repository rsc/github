// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package github

import (
	"fmt"
	"time"

	"rsc.io/github/schema"
)

func (c *Client) Projects(org, query string) ([]*Project, error) {
	commonField := `
	  createdAt
	  dataType
	  id
	  name
	  updatedAt
	`
	graphql := `
	  query($Org: String!, $Query: String, $Cursor: String) {
	    organization(login: $Org) {
	      projectsV2(first: 100, query: $Query, after: $Cursor) {
	        pageInfo {
	          hasNextPage
	          endCursor
	        }
	        totalCount
	        nodes {
	          closed
	          closedAt
	          createdAt
	          updatedAt
	          id
	          number
	          title
	          url
	          fields(first: 100) {
	            pageInfo {
	              hasNextPage
	              endCursor
	            }
	            totalCount
	            nodes {
	              __typename
	              ... on ProjectV2Field {
	                ` + commonField + `
	              }
	              ... on ProjectV2IterationField {
	                ` + commonField + `
	                configuration {
	                  completedIterations {
	                    duration
	                    id
	                    startDate
	                    title
	                    titleHTML
	                  }
	                  iterations {
	                    duration
	                    id
	                    startDate
	                    title
	                    titleHTML
	                  }
	                  duration
	                  startDay
	                }
	              }
	              ... on ProjectV2SingleSelectField {
	                ` + commonField + `
	                options {
	                  id
	                  name
	                  nameHTML
	                }
	              }
	            }
	          }
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": org}
	if query != "" {
		vars["Query"] = query
	}
	return collect(c, graphql, vars,
		toProject(org),
		func(q *schema.Query) pager[*schema.ProjectV2] { return q.Organization.ProjectsV2 },
	)
}

const projectItemFields = `
    databaseId
    fieldValues(first: 100) {
      pageInfo {
        hasNextPage
        endCursor
      }
      totalCount
      nodes {
        __typename
        ... on ProjectV2ItemFieldDateValue {
          createdAt databaseId id updatedAt
          date
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldIterationValue {
          createdAt databaseId id updatedAt
          field { __typename ... on ProjectV2IterationField { databaseId id name } }
        }
        ... on ProjectV2ItemFieldLabelValue {
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldMilestoneValue {
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldNumberValue {
          createdAt databaseId id updatedAt
          number
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldPullRequestValue {
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldRepositoryValue {
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldReviewerValue {
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldSingleSelectValue {
          createdAt databaseId id updatedAt
          name nameHTML optionId
          field { __typename ... on ProjectV2SingleSelectField { databaseId id name } }
        }
        ... on ProjectV2ItemFieldTextValue {
          createdAt databaseId id updatedAt
          text
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
        ... on ProjectV2ItemFieldUserValue {
          field { __typename ... on ProjectV2Field { databaseId id name } }
        }
      }
    }
    id
    isArchived
    type
    updatedAt
    createdAt
    content {
      __typename
      ... on Issue {
        ` + issueFields + `
      }
    }
`

func (c *Client) ProjectItems(p *Project) ([]*ProjectItem, error) {
	graphql := `
	  query($Org: String!, $ProjectNumber: Int!, $Cursor: String) {
	    organization(login: $Org) {
	      projectV2(number: $ProjectNumber) {
	        items(first: 100, after: $Cursor) {
	          pageInfo {
	            hasNextPage
	            endCursor
	          }
	          totalCount
	          nodes {
			    ` + projectItemFields + `
	          }
	        }
	      }
	    }
	  }
	`

	vars := Vars{"Org": p.Org, "ProjectNumber": p.Number}
	return collect(c, graphql, vars,
		p.toProjectItem,
		func(q *schema.Query) pager[*schema.ProjectV2Item] { return q.Organization.ProjectV2.Items },
	)
}

type Project struct {
	ID        string
	Closed    bool
	ClosedAt  time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
	Fields    []*ProjectField
	Number    int
	Title     string
	URL       string
	Org       string
}

func (p *Project) FieldByName(name string) *ProjectField {
	for _, f := range p.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func toProject(org string) func(*schema.ProjectV2) *Project {
	return func(s *schema.ProjectV2) *Project {
		// TODO: Check p.Fields.PageInfo.HasNextPage.
		return &Project{
			ID:        string(s.Id),
			Closed:    s.Closed,
			ClosedAt:  toTime(s.ClosedAt),
			CreatedAt: toTime(s.CreatedAt),
			UpdatedAt: toTime(s.UpdatedAt),
			Fields:    apply(toProjectField, s.Fields.Nodes),
			Number:    s.Number,
			Title:     s.Title,
			URL:       string(s.Url),
			Org:       org,
		}
	}
}

type ProjectField struct {
	Kind       string // "field", "iteration", "select"
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DataType   schema.ProjectV2FieldType // TODO
	DatabaseID int
	ID         schema.ID
	Name       string
	Iterations *ProjectIterations
	Options    []*ProjectFieldOption
}

func (f *ProjectField) OptionByName(name string) *ProjectFieldOption {
	for _, o := range f.Options {
		if o.Name == name {
			return o
		}
	}
	return nil
}

func toProjectField(su schema.ProjectV2FieldConfiguration) *ProjectField {
	s, _ := su.Interface.(schema.ProjectV2FieldCommon_Interface)
	f := &ProjectField{
		CreatedAt:  toTime(s.GetCreatedAt()),
		UpdatedAt:  toTime(s.GetUpdatedAt()),
		DatabaseID: s.GetDatabaseId(),
		ID:         s.GetId(),
		Name:       s.GetName(),
	}
	switch s := s.(type) {
	case *schema.ProjectV2Field:
		f.Kind = "field"
	case *schema.ProjectV2IterationField:
		f.Kind = "iteration"
		f.Iterations = toProjectIterations(s.Configuration)
	case *schema.ProjectV2SingleSelectField:
		f.Kind = "select"
		f.Options = apply(toProjectFieldOption, s.Options)
	}
	return f
}

type ProjectIterations struct {
	Completed []*ProjectIteration
	Active    []*ProjectIteration
	Days      int
	StartDay  time.Weekday
}

func toProjectIterations(s *schema.ProjectV2IterationFieldConfiguration) *ProjectIterations {
	return &ProjectIterations{
		Completed: apply(toProjectIteration, s.CompletedIterations),
		Active:    apply(toProjectIteration, s.Iterations),
		StartDay:  time.Weekday(s.StartDay),
		Days:      s.Duration,
	}
}

type ProjectIteration struct {
	Days      int
	ID        string
	Start     time.Time
	Title     string
	TitleHTML string
}

func toProjectIteration(s *schema.ProjectV2IterationFieldIteration) *ProjectIteration {
	return &ProjectIteration{
		Days:      s.Duration,
		ID:        s.Id,
		Start:     toDate(s.StartDate),
		Title:     s.Title,
		TitleHTML: s.TitleHTML,
	}
}

type ProjectFieldOption struct {
	ID       string
	Name     string
	NameHTML string
}

func (o *ProjectFieldOption) String() string {
	return fmt.Sprintf("%+v", *o)
}

func toProjectFieldOption(s *schema.ProjectV2SingleSelectFieldOption) *ProjectFieldOption {
	return &ProjectFieldOption{
		ID:       s.Id,
		Name:     s.Name,
		NameHTML: s.NameHTML,
	}
}

type ProjectItem struct {
	CreatedAt  time.Time
	DatabaseID int
	ID         schema.ID
	IsArchived bool
	Type       schema.ProjectV2ItemType
	UpdatedAt  time.Time
	Fields     []*ProjectFieldValue
	Issue      *Issue
}

func (it *ProjectItem) FieldByName(name string) *ProjectFieldValue {
	for _, f := range it.Fields {
		if f.Field == name {
			return f
		}
	}
	return nil
}

func (p *Project) toProjectItem(s *schema.ProjectV2Item) *ProjectItem {
	// TODO: Check p.Fields.PageInfo.HasNextPage.
	it := &ProjectItem{
		CreatedAt:  toTime(s.CreatedAt),
		DatabaseID: s.DatabaseId,
		ID:         s.Id,
		IsArchived: s.IsArchived,
		Type:       s.Type,
		UpdatedAt:  toTime(s.UpdatedAt),
		Fields:     apply(p.toProjectFieldValue, s.FieldValues.Nodes),
		// TODO Issue
	}
	if si, ok := s.Content.Interface.(*schema.Issue); ok {
		it.Issue = toIssue(si)
	}
	return it
}

type ProjectFieldValue struct {
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Kind       string
	ID         string
	DatabaseID int
	Field      string
	Option     *ProjectFieldOption
	Date       time.Time
	Text       string
}

func (v *ProjectFieldValue) String() string {
	switch v.Kind {
	case "date":
		return fmt.Sprintf("%s:%v", v.Field, v.Date.Format("2006-01-02"))
	case "text":
		return fmt.Sprintf("%s:%q", v.Field, v.Text)
	case "select":
		return fmt.Sprintf("%s:%q", v.Field, v.Option)
	}
	return fmt.Sprintf("%s:???", v.Field)
}

func (p *Project) optionByID(id string) *ProjectFieldOption {
	for _, f := range p.Fields {
		for _, o := range f.Options {
			if o.ID == id {
				return o
			}
		}
	}
	return nil
}

func (p *Project) toProjectFieldValue(s schema.ProjectV2ItemFieldValue) *ProjectFieldValue {
	switch sv := s.Interface.(type) {
	case *schema.ProjectV2ItemFieldDateValue:
		return &ProjectFieldValue{
			Kind:       "date",
			CreatedAt:  toTime(sv.CreatedAt),
			DatabaseID: sv.DatabaseId,
			Field:      sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
			ID:         string(sv.Id),
			UpdatedAt:  toTime(sv.UpdatedAt),
			Date:       toDate(sv.Date),
		}
	case *schema.ProjectV2ItemFieldIterationValue:
		return &ProjectFieldValue{
			Kind:       "iteration",
			CreatedAt:  toTime(sv.CreatedAt),
			DatabaseID: sv.DatabaseId,
			Field:      sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
			ID:         string(sv.Id),
			UpdatedAt:  toTime(sv.UpdatedAt),
		}
	case *schema.ProjectV2ItemFieldLabelValue:
		return &ProjectFieldValue{
			Kind:  "label",
			Field: sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
		}
	case *schema.ProjectV2ItemFieldMilestoneValue:
		return &ProjectFieldValue{
			Kind:  "milestone",
			Field: sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
		}
	case *schema.ProjectV2ItemFieldNumberValue:
		return &ProjectFieldValue{
			Kind:       "number",
			CreatedAt:  toTime(sv.CreatedAt),
			DatabaseID: sv.DatabaseId,
			Field:      sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
			ID:         string(sv.Id),
			UpdatedAt:  toTime(sv.UpdatedAt),
		}
	case *schema.ProjectV2ItemFieldPullRequestValue:
		return &ProjectFieldValue{
			Kind:  "pr",
			Field: sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
		}
	case *schema.ProjectV2ItemFieldRepositoryValue:
		return &ProjectFieldValue{
			Kind:  "repo",
			Field: sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
		}
	case *schema.ProjectV2ItemFieldReviewerValue:
		return &ProjectFieldValue{
			Kind:  "reviewer",
			Field: sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
		}
	case *schema.ProjectV2ItemFieldSingleSelectValue:
		return &ProjectFieldValue{
			Kind:       "select",
			CreatedAt:  toTime(sv.CreatedAt),
			DatabaseID: sv.DatabaseId,
			Field:      sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
			ID:         string(sv.Id),
			UpdatedAt:  toTime(sv.UpdatedAt),

			Option: p.optionByID(sv.OptionId),
		}
	case *schema.ProjectV2ItemFieldTextValue:
		return &ProjectFieldValue{
			Kind:       "text",
			CreatedAt:  toTime(sv.CreatedAt),
			DatabaseID: sv.DatabaseId,
			Field:      sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
			ID:         string(sv.Id),
			UpdatedAt:  toTime(sv.UpdatedAt),
			Text:       sv.Text,
		}
	case *schema.ProjectV2ItemFieldUserValue:
		return &ProjectFieldValue{
			Kind:  "user",
			Field: sv.Field.Interface.(schema.ProjectV2FieldCommon_Interface).GetName(),
		}
	}
	return &ProjectFieldValue{}
}
