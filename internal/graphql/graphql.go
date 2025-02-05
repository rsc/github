// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package graphql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	user   string
	passwd string
}

func Dial() (*Client, error) {
	user, passwd, err := netrcAuth("api.github.com")
	if err != nil {
		return nil, err
	}
	return &Client{user: user, passwd: passwd}, nil
}

type Vars map[string]any

func (c *Client) GraphQL(query string, vars Vars, reply any) error {
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
		return fmt.Errorf("graphql error: %s", jsreply.Errors[0].Message)
	}

	return nil
}
