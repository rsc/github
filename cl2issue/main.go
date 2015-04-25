// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Cl2issue scans Gerrit for pending CLs that mention GitHub issues
// and posts links to those CLs as GitHub issue comments.
// It expects to find golang.org/x/build/cmd/cl and rsc.io/github/issue
// in its $PATH, and it expects to have a GitHub personal access token
// in $HOME/.github-cl2issue-token for use with the issue program.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"
)

const mentionsTemplate = "CL https://golang.org/cl/%v mentions this issue."

var (
	editFlag = flag.String("edit-for-cl", "", "act as $EDITOR for issue, mentioning CL `cl`")
	flagN    = flag.Bool("n", false, "print operations but do not execute them")
)

type CL struct {
	Number int
	Issues []int
}

type Issue struct {
	Comments []*Comment
}

type Comment struct {
	Text string
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("cl2issue: ")
	flag.Parse()

	if *editFlag != "" {
		runEditor()
		return
	}

	args := []string{"-json"}
	args = append(args, flag.Args()...)
	data, err := exec.Command("cl", args...).CombinedOutput()
	if err != nil {
		log.Fatal("fetching CLs: %v\n%s", err, data)
	}

	var cls []*CL
	if err := json.Unmarshal(data, &cls); err != nil {
		log.Fatal("parsing CLs: %v", err)
	}

	tokenFile := os.Getenv("HOME") + "/.github-cl2issue-token"
	for _, cl := range cls {
		mentions := fmt.Sprintf(mentionsTemplate, cl.Number)
	Issues:
		for _, issueNumber := range cl.Issues {
			data, err := exec.Command("issue", "-token", tokenFile, "-json", fmt.Sprint(issueNumber)).CombinedOutput()
			if err != nil {
				log.Printf("reading #%d: %v\n%s", issueNumber, err, data)
				continue
			}
			var issue Issue
			if err := json.Unmarshal(data, &issue); err != nil {
				log.Printf("parsing #%d: %v", issueNumber, err)
				continue
			}
			for _, com := range issue.Comments {
				if strings.Contains(com.Text, mentions) {
					continue Issues
				}
			}
			fmt.Printf("post to #%d about CL %d\n", issueNumber, cl.Number)
			if *flagN {
				continue
			}
			cmd := exec.Command("issue", "-token", tokenFile, "-e", fmt.Sprint(issueNumber))
			cmd.Env = editorEnv(cl.Number)
			data, err = cmd.CombinedOutput()
			if err != nil {
				log.Printf("updating #%d: %v\n%s", issueNumber, err, data)
				continue
			}
		}
	}
}

func runEditor() {
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	file := flag.Arg(0)
	data, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}

	i := bytes.Index(data, []byte("\nReported by "))
	if i < 0 {
		log.Fatal("unexpected issue template")
	}

	newdata := append(data[:i:i], []byte(fmt.Sprintf("\n\n"+mentionsTemplate+"\n\n", *editFlag))...)
	newdata = append(newdata, data[i:]...)

	if err := ioutil.WriteFile(file, newdata, 0666); err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}

func editorEnv(cl int) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "EDITOR=") || strings.HasPrefix(kv, "VISUAL=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "EDITOR=cl2issue -edit-for-cl "+fmt.Sprint(cl))
	return env
}
