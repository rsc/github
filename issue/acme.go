// Copyright 2013 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Originally code.google.com/p/rsc/cmd/issue/acme.go.

package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"9fans.net/go/acme"
	"9fans.net/go/plumb"
	"github.com/google/go-github/v62/github"
)

const root = "/issue/"

func (w *awin) project() string {
	p := w.prefix
	p = strings.TrimPrefix(p, root)
	i := strings.Index(p, "/")
	if i >= 0 {
		j := strings.Index(p[i+1:], "/")
		if j >= 0 {
			p = p[:i+1+j]
		}
	}
	return p
}

func acmeMode() {
	var dummy awin
	dummy.prefix = path.Join(root, *project) + "/"
	if flag.NArg() > 0 {
		// TODO(rsc): Without -a flag, the query is conatenated into one query.
		// Decide which behavior should be used, and use it consistently.
		// TODO(rsc): Block this look from doing the multiline selection mode?
		for _, arg := range flag.Args() {
			if dummy.Look(arg) {
				continue
			}
			if arg == "new" {
				dummy.createIssue()
				continue
			}
			dummy.newSearch(dummy.prefix, "search", arg)
		}
	} else {
		dummy.Look("all")
	}

	go plumbserve()

	select {}
}

func plumbserve() {
	fid, err := plumb.Open("githubissue", 0)
	if err != nil {
		acme.Errf(root, "plumb: %v", err)
		return
	}
	r := bufio.NewReader(fid)
	for {
		var m plumb.Message
		if err := m.Recv(r); err != nil {
			acme.Errf(root, "plumb recv: %v", err)
			return
		}
		if m.Type != "text" {
			acme.Errf(root, "plumb recv: unexpected type: %s", m.Type)
			continue
		}
		if m.Dst != "githubissue" {
			acme.Errf(root, "plumb recv: unexpected dst: %s", m.Dst)
			continue
		}
		// TODO use m.Dir
		data := string(m.Data)
		var project, what string
		if strings.HasPrefix(data, root) {
			project = data[len(root):]
			i := strings.LastIndex(project, "/")
			if i < 0 {
				acme.Errf(root, "plumb recv: bad text %q", data)
				continue
			}
			project, what = project[:i], project[i+1:]
		} else {
			i := strings.Index(data, "#")
			if i < 0 {
				acme.Errf(root, "plumb recv: bad text %q", data)
				continue
			}
			project, what = data[:i], data[i+1:]
		}
		if strings.Count(project, "/") != 1 {
			acme.Errf(root, "plumb recv: bad text %q", data)
			continue
		}
		var plummy awin
		plummy.prefix = path.Join(root, project) + "/"
		if !plummy.Look(what) {
			acme.Errf(root, "plumb recv: can't look %s%s", plummy.prefix, what)
		}
	}
}

const (
	modeSingle = 1 + iota
	modeQuery
	modeCreate
	modeMilestone
	modeBulk
)

type awin struct {
	*acme.Win
	prefix       string
	mode         int
	query        string
	id           int
	github       *github.Issue
	title        string
	sortByNumber bool // otherwise sort by title
}

var all struct {
	sync.Mutex
	m map[*acme.Win]*awin
}

func (w *awin) exit() {
	all.Lock()
	defer all.Unlock()
	if all.m[w.Win] == w {
		delete(all.m, w.Win)
	}
	if len(all.m) == 0 {
		os.Exit(0)
	}
}

func (w *awin) new(prefix, title string) *awin {
	all.Lock()
	defer all.Unlock()
	if all.m == nil {
		all.m = make(map[*acme.Win]*awin)
	}
	w1 := new(awin)
	w1.title = title
	var err error
	w1.Win, err = acme.New()
	if err != nil {
		log.Printf("creating acme window: %v", err)
		time.Sleep(10 * time.Millisecond)
		w1.Win, err = acme.New()
		if err != nil {
			log.Fatalf("creating acme window again: %v", err)
		}
	}
	w1.prefix = prefix
	w1.SetErrorPrefix(w1.prefix)
	w1.Name(w1.prefix + title)
	all.m[w1.Win] = w1
	return w1
}

func (w *awin) show(title string) bool {
	return acme.Show(w.prefix+title) != nil
}

var numRE = regexp.MustCompile(`(?m)^#[0-9]+\t`)
var repoHashRE = regexp.MustCompile(`\A([A-Za-z0-9_]+/[A-Za-z0-9_]+)#(all|[0-9]+)\z`)

var milecache struct {
	sync.Mutex
	list map[string][]*github.Milestone
}

func cachedMilestones(project string) []*github.Milestone {
	milecache.Lock()
	if milecache.list == nil {
		milecache.list = make(map[string][]*github.Milestone)
	}
	if milecache.list[project] == nil {
		milecache.list[project], _ = loadMilestones(project)
	}
	list := milecache.list[project]
	milecache.Unlock()
	return list
}

func (w *awin) Look(text string) bool {
	ids := readBulkIDs([]byte(text))
	if len(ids) > 0 {
		for _, id := range ids {
			text := fmt.Sprint(id)
			if w.show(text) {
				continue
			}
			w.newIssue(w.prefix, text, id)
		}
		return true
	}

	if text == "all" {
		if w.show("all") {
			return true
		}
		w.newSearch(w.prefix, "all", "")
		return true
	}
	if text == "Milestone" || text == "Milestones" || text == "milestone" {
		if w.show("milestone") {
			return true
		}
		w.newMilestoneList()
		return true
	}
	list := cachedMilestones(w.project())
	for _, m := range list {
		if getString(m.Title) == text {
			if w.show(text) {
				return true
			}
			w.newSearch(w.prefix, text, "milestone:"+text)
			return true
		}
	}

	if n, _ := strconv.Atoi(strings.TrimPrefix(text, "#")); 0 < n && n < 1000000 {
		text = strings.TrimPrefix(text, "#")
		if w.show(text) {
			return true
		}
		w.newIssue(w.prefix, text, n)
		return true
	}

	if m := repoHashRE.FindStringSubmatch(text); m != nil {
		project := m[1]
		what := m[2]
		prefix := path.Join(root, project) + "/"
		if acme.Show(prefix+what) != nil {
			return true
		}
		if what == "all" {
			w.newSearch(prefix, what, "")
			return true
		}
		if n, _ := strconv.Atoi(what); 0 < n && n < 1000000 {
			w.newIssue(prefix, what, n)
			return true
		}
		return false
	}

	if m := numRE.FindAllString(text, -1); m != nil {
		for _, s := range m {
			w.Look(strings.TrimSpace(strings.TrimPrefix(s, "#")))
		}
		return true
	}
	return false
}

func (w *awin) setMilestone(milestone, text string) {
	var buf bytes.Buffer
	id := findMilestone(&buf, w.project(), &milestone)
	if buf.Len() > 0 {
		w.Err(strings.TrimSpace(buf.String()))
	}
	if id == nil {
		return
	}
	milestoneID := *id

	stop := w.Blink()
	defer stop()
	if w.mode == modeSingle {
		w.setMilestone1(milestoneID, w.id)
		w.load()
		return
	}
	if n, _ := strconv.Atoi(strings.TrimPrefix(text, "#")); 0 < n && n < 100000 {
		w.setMilestone1(milestoneID, n)
		return
	}
	if m := numRE.FindAllString(text, -1); m != nil {
		for _, s := range m {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "#")))
			if 0 < n && n < 100000 {
				w.setMilestone1(milestoneID, n)
			}
		}
		return
	}
}

func (w *awin) setMilestone1(milestoneID, n int) {
	var edit github.IssueRequest
	edit.Milestone = &milestoneID

	_, _, err := client.Issues.Edit(context.TODO(), projectOwner(w.project()), projectRepo(w.project()), n, &edit)
	if err != nil {
		w.Err(fmt.Sprintf("Error changing issue #%d: %v", n, err))
	}
}

func (w *awin) createIssue() {
	w = w.new(w.prefix, "new")
	w.mode = modeCreate
	w.Ctl("cleartag")
	w.Fprintf("tag", " Put Search ")
	go w.load()
	go w.loop()
}

func (w *awin) newIssue(prefix, title string, id int) {
	w = w.new(prefix, title)
	w.mode = modeSingle
	w.id = id
	w.Ctl("cleartag")
	w.Fprintf("tag", " Get Put Look ")
	go w.load()
	go w.loop()
}

func (w *awin) newBulkEdit(body []byte) {
	w = w.new(w.prefix, "bulk-edit/")
	w.mode = modeBulk
	w.query = ""
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Sort Search ")
	w.Write("body", append([]byte("Loading...\n\n"), body...))
	go w.load()
	go w.loop()
}

func (w *awin) newMilestoneList() {
	w = w.new(w.prefix, "milestone")
	w.mode = modeMilestone
	w.query = ""
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Sort Search ")
	w.Write("body", []byte("Loading..."))
	go w.load()
	go w.loop()
}

func (w *awin) newSearch(prefix, title, query string) {
	w = w.new(prefix, title)
	w.mode = modeQuery
	w.query = query
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Bulk Sort Search ")
	w.Write("body", []byte("Loading..."))
	go w.load()
	go w.loop()
}

var createTemplate = `Title:
Assignee:
Labels:
Milestone:

<describe issue here>

`

func (w *awin) load() {
	switch w.mode {
	case modeCreate:
		w.Clear()
		w.Write("body", []byte(createTemplate))
		w.Ctl("clean")

	case modeSingle:
		var buf bytes.Buffer
		stop := w.Blink()
		issue, err := showIssue(&buf, w.project(), w.id)
		stop()
		w.Clear()
		if err != nil {
			w.Write("body", []byte(err.Error()))
			break
		}
		w.Write("body", buf.Bytes())
		w.Ctl("clean")
		w.github = issue

	case modeMilestone:
		stop := w.Blink()
		milestones, err := loadMilestones(w.project())
		milecache.Lock()
		if milecache.list == nil {
			milecache.list = make(map[string][]*github.Milestone)
		}
		milecache.list[w.project()] = milestones
		milecache.Unlock()
		stop()
		w.Clear()
		if err != nil {
			w.Fprintf("body", "Error loading milestones: %v\n", err)
			break
		}
		var buf bytes.Buffer
		for _, m := range milestones {
			fmt.Fprintf(&buf, "%s\t%s\t%d\n", getTime(m.DueOn).Format("2006-01-02"), getString(m.Title), getInt(m.OpenIssues))
		}
		w.PrintTabbed(buf.String())
		w.Ctl("clean")

	case modeQuery:
		var buf bytes.Buffer
		stop := w.Blink()
		err := showQuery(&buf, w.project(), w.query)
		if w.title == "all" {
			cachedMilestones(w.project())
		}
		stop()
		w.Clear()
		if err != nil {
			w.Write("body", []byte(err.Error()))
			break
		}
		if w.title == "all" {
			var names []string
			for _, m := range cachedMilestones(w.project()) {
				names = append(names, getString(m.Title))
			}
			if len(names) > 0 {
				w.Fprintf("body", "Milestones: %s\n\n", strings.Join(names, " "))
			}
		}
		if w.title == "search" {
			w.Fprintf("body", "Search %s\n\n", w.query)
		}
		w.PrintTabbed(buf.String())
		w.Ctl("clean")

	case modeBulk:
		stop := w.Blink()
		body, err := w.ReadAll("body")
		if err != nil {
			w.Err(fmt.Sprintf("%v", err))
			stop()
			break
		}
		base, original, err := bulkEditStartFromText(w.project(), body)
		stop()
		if err != nil {
			w.Err(fmt.Sprintf("%v", err))
			break
		}
		w.Clear()
		w.PrintTabbed(string(original))
		w.Ctl("clean")
		w.github = base
	}

	w.Addr("0")
	w.Ctl("dot=addr")
	w.Ctl("show")
}

func diff(line, field, old string) *string {
	old = strings.TrimSpace(old)
	line = strings.TrimSpace(strings.TrimPrefix(line, field))
	if old == line {
		return nil
	}
	return &line
}

func (w *awin) put() {
	stop := w.Blink()
	defer stop()
	switch w.mode {
	case modeSingle, modeCreate:
		old := w.github
		if w.mode == modeCreate {
			old = new(github.Issue)
		}
		data, err := w.ReadAll("body")
		if err != nil {
			w.Err(fmt.Sprintf("Put: %v", err))
			return
		}
		issue, _, err := writeIssue(w.project(), old, data, false)
		if err != nil {
			w.Err(err.Error())
			return
		}
		if w.mode == modeCreate {
			w.mode = modeSingle
			w.id = getInt(issue.Number)
			w.title = fmt.Sprint(w.id)
			w.Name(w.prefix + w.title)
			w.github = issue
		}
		w.load()

	case modeBulk:
		data, err := w.ReadAll("body")
		if err != nil {
			w.Err(fmt.Sprintf("Put: %v", err))
			return
		}
		ids, err := bulkWriteIssue(w.project(), w.github, data, func(s string) { w.Err("Put: " + s) })
		if err != nil {
			errText := strings.Replace(err.Error(), "\n", "\t\n", -1)
			if len(ids) > 0 {
				w.Err(fmt.Sprintf("updated %d issue%s with errors:\n\t%v", len(ids), suffix(len(ids)), errText))
				break
			}
			w.Err(fmt.Sprintf("%s", errText))
			break
		}
		w.Err(fmt.Sprintf("updated %d issue%s", len(ids), suffix(len(ids))))

	case modeMilestone:
		w.Err("cannot Put milestone list")

	case modeQuery:
		w.Err("cannot Put issue list")
	}
}

func (w *awin) sort() {
	if err := w.Addr("0/^[0-9]/,"); err != nil {
		w.Err("nothing to sort")
	}
	var less func(string, string) bool
	if w.sortByNumber {
		less = func(x, y string) bool { return lineNumber(x) > lineNumber(y) }
	} else {
		less = func(x, y string) bool { return skipField(x) < skipField(y) }
	}
	if err := w.Sort(less); err != nil {
		w.Err(err.Error())
	}
	w.Addr("0")
	w.Ctl("dot=addr")
	w.Ctl("show")
}

func lineNumber(s string) int {
	n := 0
	for j := 0; j < len(s) && '0' <= s[j] && s[j] <= '9'; j++ {
		n = n*10 + int(s[j]-'0')
	}
	return n
}

func skipField(s string) string {
	i := strings.Index(s, "\t")
	if i < 0 {
		return s
	}
	for i < len(s) && s[i+1] == '\t' {
		i++
	}
	return s[i:]
}

func (w *awin) Execute(cmd string) bool {
	switch cmd {
	case "Get":
		w.load()
		return true
	case "Put":
		w.put()
		return true
	case "Del":
		w.Ctl("del")
		return true
	case "New":
		w.createIssue()
		return true
	case "Sort":
		if w.mode != modeQuery {
			w.Err("can only sort issue list windows")
			break
		}
		w.sortByNumber = !w.sortByNumber
		w.sort()
		return true
	case "Bulk":
		// TODO(rsc): If Bulk has an argument, treat as search query and use results?
		if w.mode != modeQuery {
			w.Err("can only start bulk edit in issue list windows")
			return true
		}
		text := w.Selection()
		if text == "" {
			data, err := w.ReadAll("body")
			if err != nil {
				w.Err(fmt.Sprintf("%v", err))
				return true
			}
			text = string(data)
		}
		w.newBulkEdit([]byte(text))
		return true
	}

	if strings.HasPrefix(cmd, "Search ") {
		w.newSearch(w.prefix, "search", strings.TrimSpace(strings.TrimPrefix(cmd, "Search")))
		return true
	}
	if strings.HasPrefix(cmd, "Milestone ") {
		text := w.Selection()
		w.setMilestone(strings.TrimSpace(strings.TrimPrefix(cmd, "Milestone")), text)
		return true
	}

	return false
}

func (w *awin) loop() {
	defer w.exit()
	w.EventLoop(w)
}
