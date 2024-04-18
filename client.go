// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package github provides idiomatic Go APIs for accessing basic GitHub issue operations.
//
// The entire GitHub API can be accessed by using the [Client] with GraphQL schema from
// [rsc.io/github/schema].
package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"rsc.io/github/schema"
)

// A Client is an authenticated client for accessing the GitHub GraphQL API.
// Client provides convenient methods for common operations.
// To build others, see the [GraphQLQuery] and [GraphQLMutation] methods.
type Client struct {
	user   string
	passwd string
}

// Dial returns a Client authenticating as user.
// Authentication credentials are loaded from $HOME/.netrc
// using the 'api.github.com' entry, which should contain a
// GitHub personal access token.
// If user is the empty string, Dial uses the first line in .netrc
// listed for api.github.com.
//
// For example, $HOME/.netrc might contain:
//
//	machine api.github.com login ken password ghp_123456789abcdef123456789abcdef12345
func Dial(user string) (*Client, error) {
	user, passwd, err := netrcAuth("api.github.com", user)
	if err != nil {
		return nil, err
	}
	return &Client{user: user, passwd: passwd}, nil
}

// NewClient returns a new client authenticating as the given GitHub user
// with the given GitHub personal access token (of the form "ghp_....").
func NewClient(user, token string) *Client {
	return &Client{user: user, passwd: token}
}

// A Vars is a binding of GraphQL variables to JSON-able values (usually strings).
type Vars map[string]any

// GraphQLQuery runs a single query with the bound variables.
// For example, to look up a repository ID:
//
//	func repoID(org, name string) (string, error) {
//		graphql := `
//		  query($Org: String!, $Repo: String!) {
//		    repository(owner: $Org, name: $Repo) {
//		      id
//		    }
//		  }
//		`
//		vars := Vars{"Org": org, "Repo": repo}
//		q, err := c.GraphQLQuery(graphql, vars)
//		if err != nil {
//			return "", err
//		}
//		return string(q.Repository.Id), nil
//	}
//
// (This is roughly the implementation of the [Client.Repo] method.)
func (c *Client) GraphQLQuery(query string, vars Vars) (*schema.Query, error) {
	var reply schema.Query
	if err := c.graphQL(query, vars, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// GraphQLMutation runs a single mutation with the bound variables.
// For example, to edit an issue comment:
//
//	func editComment(commentID, body string) error {
//		graphql := `
//		  mutation($Comment: ID!, $Body: String!) {
//		    updateIssueComment(input: {id: $Comment, body: $Body}) {
//		      clientMutationId
//		    }
//		  }
//		`
//		_, err := c.GraphQLMutation(graphql, Vars{"Comment": commentID, "Body": body})
//		return err
//	}
//
// (This is roughly the implementation of the [Client.EditIssueComment] method.)
func (c *Client) GraphQLMutation(query string, vars Vars) (*schema.Mutation, error) {
	var reply schema.Mutation
	if err := c.graphQL(query, vars, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

func (c *Client) graphQL(query string, vars Vars, reply any) error {
	js, err := json.Marshal(struct {
		Query     string `json:"query"`
		Variables any    `json:"variables"`
	}{
		Query:     query,
		Variables: vars,
	})
	if err != nil {
		return err
	}

Retry:
	method := "POST"
	body := bytes.NewReader(js)
	if query == "schema" && vars == nil {
		method = "GET"
		js = nil
	}
	req, err := http.NewRequest(method, "https://api.github.com/graphql", body)
	if err != nil {
		return err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.passwd)
	}

	previews := []string{
		"application/vnd.github.inertia-preview+json", // projects
		"application/vnd.github.starfox-preview+json", // projects events
		"application/vnd.github.elektra-preview+json", // pinned issues
	}
	req.Header.Set("Accept", strings.Join(previews, ","))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading body: %v", err)
	}
	if resp.StatusCode != 200 {
		err := fmt.Errorf("%s\n%s", resp.Status, data)
		// TODO(rsc): Could do better here, but this works reasonably well.
		// If we're over quota, it could be a while.
		if strings.Contains(err.Error(), "wait a few minutes") {
			log.Printf("github: %v", err)
			time.Sleep(10 * time.Minute)
			goto Retry
		}
		return err
	}

	jsreply := struct {
		Data   any
		Errors []struct {
			Message string
		}
	}{
		Data: reply,
	}

	err = json.Unmarshal(data, &jsreply)
	if err != nil {
		return fmt.Errorf("parsing reply: %v", err)
	}

	if len(jsreply.Errors) > 0 {
		if strings.Contains(jsreply.Errors[0].Message, "rate limit exceeded") {
			log.Printf("github: %s", jsreply.Errors[0].Message)
			time.Sleep(10 * time.Minute)
			goto Retry
		}
		if strings.Contains(jsreply.Errors[0].Message, "submitted too quickly") {
			log.Printf("github: %s", jsreply.Errors[0].Message)
			time.Sleep(5 * time.Second)
			goto Retry
		}
		for i, line := range strings.Split(query, "\n") {
			log.Print(i+1, line)
		}
		return fmt.Errorf("graphql error: %s", jsreply.Errors[0].Message)
	}

	return nil
}

func collect[Schema, Out any](c *Client, graphql string, vars Vars, transform func(Schema) Out,
	page func(*schema.Query) pager[Schema]) ([]Out, error) {
	var cursor string
	var list []Out
	for {
		if cursor != "" {
			vars["Cursor"] = cursor
		}
		q, err := c.GraphQLQuery(graphql, vars)
		if err != nil {
			return list, err
		}
		p := page(q)
		if p == nil {
			break
		}
		list = append(list, apply(transform, p.GetNodes())...)
		info := p.GetPageInfo()
		cursor = info.EndCursor
		if cursor == "" || !info.HasNextPage {
			break
		}
	}
	return list, nil
}

type pager[T any] interface {
	GetPageInfo() *schema.PageInfo
	GetNodes() []T
}

func apply[In, Out any](f func(In) Out, x []In) []Out {
	var out []Out
	for _, in := range x {
		out = append(out, f(in))
	}
	return out
}

func toTime(s schema.DateTime) time.Time {
	t, err := time.ParseInLocation(time.RFC3339Nano, string(s), time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

func toDate(s schema.Date) time.Time {
	t, err := time.ParseInLocation("2006-01-02", string(s), time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}
