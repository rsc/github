// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
)

func editIssue(project string, original []byte, issue *github.Issue) {
	updated := editText(original)
	if bytes.Equal(original, updated) {
		log.Print("no changes made")
		return
	}

	newIssue, _, err := writeIssue(project, issue, updated, false)
	if err != nil {
		log.Fatal(err)
	}
	if newIssue != nil {
		issue = newIssue
	}
	log.Printf("https://github.com/%s/issues/%d updated", project, getInt(issue.Number))
}

func editText(original []byte) []byte {
	f, err := ioutil.TempFile("", "issue-edit-")
	if err != nil {
		log.Fatal(err)
	}
	if err := ioutil.WriteFile(f.Name(), original, 0600); err != nil {
		log.Fatal(err)
	}
	if err := runEditor(f.Name()); err != nil {
		log.Fatal(err)
	}
	updated, err := ioutil.ReadFile(f.Name())
	if err != nil {
		log.Fatal(err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return updated
}

func runEditor(filename string) error {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "ed"
	}

	// If the editor contains spaces or other magic shell chars,
	// invoke it as a shell command. This lets people have
	// environment variables like "EDITOR=emacs -nw".
	// The magic list of characters and the idea of running
	// sh -c this way is taken from git/run-command.c.
	var cmd *exec.Cmd
	if strings.ContainsAny(ed, "|&;<>()$`\\\"' \t\n*?[#~=%") {
		cmd = exec.Command("sh", "-c", ed+` "$@"`, "$EDITOR", filename)
	} else {
		cmd = exec.Command(ed, filename)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("invoking editor: %v", err)
	}
	return nil
}

const bulkHeader = "\nBulk editing these issues:"

func writeIssue(project string, old *github.Issue, updated []byte, isBulk bool) (issue *github.Issue, rate *github.Rate, err error) {
	var errbuf bytes.Buffer
	defer func() {
		if errbuf.Len() > 0 {
			err = errors.New(strings.TrimSpace(errbuf.String()))
		}
	}()

	sdata := string(updated)
	off := 0
	var edit github.IssueRequest
	var addLabels, removeLabels []string
	for _, line := range strings.SplitAfter(sdata, "\n") {
		off += len(line)
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		switch {
		case strings.HasPrefix(line, "#"):
			continue

		case strings.HasPrefix(line, "Title:"):
			edit.Title = diff(line, "Title:", getString(old.Title))

		case strings.HasPrefix(line, "State:"):
			edit.State = diff(line, "State:", getString(old.State))

		case strings.HasPrefix(line, "Assignee:"):
			edit.Assignee = diff(line, "Assignee:", getUserLogin(old.Assignee))

		case strings.HasPrefix(line, "Closed:"):
			continue

		case strings.HasPrefix(line, "Labels:"):
			if isBulk {
				addLabels, removeLabels = diffList2(line, "Labels:", getLabelNames(old.Labels))
			} else {
				edit.Labels = diffList(line, "Labels:", getLabelNames(old.Labels))
			}

		case strings.HasPrefix(line, "Milestone:"):
			edit.Milestone = findMilestone(&errbuf, project, diff(line, "Milestone:", getMilestoneTitle(old.Milestone)))

		case strings.HasPrefix(line, "URL:"):
			continue

		default:
			fmt.Fprintf(&errbuf, "unknown summary line: %s\n", line)
		}
	}

	if errbuf.Len() > 0 {
		return nil, nil, nil
	}

	if getInt(old.Number) == 0 {
		comment := strings.TrimSpace(sdata[off:])
		edit.Body = &comment
		issue, resp, err := client.Issues.Create(context.TODO(), projectOwner(project), projectRepo(project), &edit)
		if resp != nil {
			rate = &resp.Rate
		}
		if err != nil {
			fmt.Fprintf(&errbuf, "error creating issue: %v\n", err)
			return nil, rate, nil
		}
		return issue, rate, nil
	}

	if getInt(old.Number) == -1 {
		// Asking to just sanity check the text parsing.
		return nil, nil, nil
	}

	marker := "\nReported by "
	if isBulk {
		marker = bulkHeader
	}
	var comment string
	if i := strings.Index(sdata, marker); i >= off {
		comment = strings.TrimSpace(sdata[off:i])
	}

	if comment == "<optional comment here>" {
		comment = ""
	}

	var failed bool
	var did []string
	if comment != "" {
		_, resp, err := client.Issues.CreateComment(context.TODO(), projectOwner(project), projectRepo(project), getInt(old.Number), &github.IssueComment{
			Body: &comment,
		})
		if resp != nil {
			rate = &resp.Rate
		}
		if err != nil {
			fmt.Fprintf(&errbuf, "error saving comment: %v\n", err)
			failed = true
		} else {
			did = append(did, "saved comment")
		}
	}

	if edit.Title != nil || edit.State != nil || edit.Assignee != nil || edit.Labels != nil || edit.Milestone != nil {
		_, resp, err := client.Issues.Edit(context.TODO(), projectOwner(project), projectRepo(project), getInt(old.Number), &edit)
		if resp != nil {
			rate = &resp.Rate
		}
		if err != nil {
			fmt.Fprintf(&errbuf, "error changing metadata: %v\n", err)
			failed = true
		} else {
			did = append(did, "updated metadata")
		}
	}
	if len(addLabels) > 0 {
		_, resp, err := client.Issues.AddLabelsToIssue(context.TODO(), projectOwner(project), projectRepo(project), getInt(old.Number), addLabels)
		if resp != nil {
			rate = &resp.Rate
		}
		if err != nil {
			fmt.Fprintf(&errbuf, "error adding labels: %v\n", err)
			failed = true
		} else {
			if len(addLabels) == 1 {
				did = append(did, "added label "+addLabels[0])
			} else {
				did = append(did, "added labels")
			}
		}
	}
	if len(removeLabels) > 0 {
		for _, label := range removeLabels {
			resp, err := client.Issues.RemoveLabelForIssue(context.TODO(), projectOwner(project), projectRepo(project), getInt(old.Number), label)
			if resp != nil {
				rate = &resp.Rate
			}
			if err != nil {
				fmt.Fprintf(&errbuf, "error removing label %s: %v\n", label, err)
				failed = true
			} else {
				did = append(did, "removed label "+label)
			}
		}
	}

	if failed && len(did) > 0 {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "%s", did[0])
		for i := 1; i < len(did)-1; i++ {
			fmt.Fprintf(&buf, ", %s", did[i])
		}
		if len(did) >= 2 {
			if len(did) >= 3 {
				fmt.Fprintf(&buf, ",")
			}
			fmt.Fprintf(&buf, " and %s", did[len(did)-1])
		}
		all := buf.Bytes()
		all[0] -= 'a' - 'A'
		fmt.Fprintf(&errbuf, "(%s successfully.)\n", all)
	}
	return
}

func diffList(line, field string, old []string) *[]string {
	line = strings.TrimSpace(strings.TrimPrefix(line, field))
	had := make(map[string]bool)
	for _, f := range old {
		had[f] = true
	}
	changes := false
	for _, f := range strings.Fields(line) {
		if !had[f] {
			changes = true
		}
		delete(had, f)
	}
	if len(had) != 0 {
		changes = true
	}
	if changes {
		ret := strings.Fields(line)
		if ret == nil {
			ret = []string{}
		}
		return &ret
	}
	return nil
}

func diffList2(line, field string, old []string) (added, removed []string) {
	line = strings.TrimSpace(strings.TrimPrefix(line, field))
	had := make(map[string]bool)
	for _, f := range old {
		had[f] = true
	}
	for _, f := range strings.Fields(line) {
		if !had[f] {
			added = append(added, f)
		}
		delete(had, f)
	}
	if len(had) != 0 {
		for _, f := range old {
			if had[f] {
				removed = append(removed, f)
			}
		}
	}
	return
}

func findMilestone(w io.Writer, project string, name *string) *int {
	if name == nil {
		return nil
	}

	all, err := loadMilestones(project)
	if err != nil {
		fmt.Fprintf(w, "Error loading milestone list: %v\n\tIgnoring milestone change.\n", err)
		return nil
	}

	for _, m := range all {
		if getString(m.Title) == *name {
			return m.Number
		}
	}

	fmt.Fprintf(w, "Unknown milestone: %s\n", *name)
	return nil
}

func readBulkIDs(text []byte) []int {
	var ids []int
	for _, line := range strings.Split(string(text), "\n") {
		if i := strings.Index(line, "\t"); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, " "); i >= 0 {
			line = line[:i]
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		ids = append(ids, n)
	}
	return ids
}

func bulkEditStartFromText(project string, content []byte) (base *github.Issue, original []byte, err error) {
	ids := readBulkIDs(content)
	if len(ids) == 0 {
		return nil, nil, fmt.Errorf("found no issues in selection")
	}
	issues, err := bulkReadIssuesCached(project, ids)
	if err != nil {
		return nil, nil, err
	}
	base, original = bulkEditStart(issues)
	return base, original, nil
}

func suffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func bulkEditIssues(project string, issues []*github.Issue) {
	base, original := bulkEditStart(issues)
	updated := editText(original)
	if bytes.Equal(original, updated) {
		log.Print("no changes made")
		return
	}
	ids, err := bulkWriteIssue(project, base, updated, func(s string) { log.Print(s) })
	if err != nil {
		errText := strings.Replace(err.Error(), "\n", "\t\n", -1)
		if len(ids) > 0 {
			log.Fatal("updated %d issue%s with errors:\n\t%v", len(ids), suffix(len(ids)), errText)
		}
		log.Fatal(errText)
	}
	log.Printf("updated %d issue%s", len(ids), suffix)
}

func bulkEditStart(issues []*github.Issue) (*github.Issue, []byte) {
	common := new(github.Issue)
	for i, issue := range issues {
		if i == 0 {
			common.State = issue.State
			common.Assignee = issue.Assignee
			common.Labels = issue.Labels
			common.Milestone = issue.Milestone
			continue
		}
		if common.State != nil && getString(common.State) != getString(issue.State) {
			common.State = nil
		}
		if common.Assignee != nil && getUserLogin(common.Assignee) != getUserLogin(issue.Assignee) {
			common.Assignee = nil
		}
		if common.Milestone != nil && getMilestoneTitle(common.Milestone) != getMilestoneTitle(issue.Milestone) {
			common.Milestone = nil
		}
		common.Labels = commonLabels(common.Labels, issue.Labels)
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "State: %s\n", getString(common.State))
	fmt.Fprintf(&buf, "Assignee: %s\n", getUserLogin(common.Assignee))
	fmt.Fprintf(&buf, "Labels: %s\n", strings.Join(getLabelNames(common.Labels), " "))
	fmt.Fprintf(&buf, "Milestone: %s\n", getMilestoneTitle(common.Milestone))
	fmt.Fprintf(&buf, "\n<optional comment here>\n")
	fmt.Fprintf(&buf, "%s\n", bulkHeader)
	for _, issue := range issues {
		fmt.Fprintf(&buf, "%d\t%s\n", getInt(issue.Number), getString(issue.Title))
	}

	return common, buf.Bytes()
}

func commonString(x, y string) string {
	if x != y {
		x = ""
	}
	return x
}

func commonLabels(x, y []github.Label) []github.Label {
	if len(x) == 0 || len(y) == 0 {
		return nil
	}
	have := make(map[string]bool)
	for _, lab := range y {
		have[getString(lab.Name)] = true
	}
	var out []github.Label
	for _, lab := range x {
		if have[getString(lab.Name)] {
			out = append(out, lab)
		}
	}
	return out
}

func bulkWriteIssue(project string, old *github.Issue, updated []byte, status func(string)) (ids []int, err error) {
	i := bytes.Index(updated, []byte(bulkHeader))
	if i < 0 {
		return nil, fmt.Errorf("cannot find bulk edit issue list")
	}
	ids = readBulkIDs(updated[i:])
	if len(ids) == 0 {
		return nil, fmt.Errorf("found no issues in bulk edit issue list")
	}

	// Make a copy of the issue to modify.
	x := *old
	old = &x

	// Try a write to issue -1, checking for formatting only.
	old.Number = new(int)
	*old.Number = -1
	_, rate, err := writeIssue(project, old, updated, true)
	if err != nil {
		return nil, err
	}

	// Apply to all issues in list.
	suffix := ""
	if len(ids) != 1 {
		suffix = "s"
	}
	status(fmt.Sprintf("updating %d issue%s", len(ids), suffix))

	failed := false
	for index, number := range ids {
		if index%10 == 0 && index > 0 {
			status(fmt.Sprintf("updated %d/%d issues", index, len(ids)))
		}
		// Check rate limits here (in contrast to everywhere else in this program)
		// to avoid needless failure halfway through the loop.
		for rate != nil && rate.Limit > 0 && rate.Remaining == 0 {
			delta := (rate.Reset.Sub(time.Now())/time.Minute + 2) * time.Minute
			if delta < 0 {
				delta = 2 * time.Minute
			}
			status(fmt.Sprintf("updated %d/%d issues; pausing %d minutes to respect GitHub rate limit", index, len(ids), int(delta/time.Minute)))
			time.Sleep(delta)
			limits, _, err := client.RateLimits(context.TODO())
			if err != nil {
				status(fmt.Sprintf("reading rate limit: %v", err))
			}
			rate = nil
			if limits != nil {
				rate = limits.Core
			}
		}
		*old.Number = number
		if _, rate, err = writeIssue(project, old, updated, true); err != nil {
			status(fmt.Sprintf("writing #%d: %s", number, strings.Replace(err.Error(), "\n", "\n\t", -1)))
			failed = true
		}
	}

	if failed {
		return ids, fmt.Errorf("failed to update all issues")
	}
	return ids, nil
}

func projectOwner(project string) string {
	return project[:strings.Index(project, "/")]
}

func projectRepo(project string) string {
	return project[strings.Index(project, "/")+1:]
}
