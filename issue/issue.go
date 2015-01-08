// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Issue is a client for reading GitHub project issues.
//
//	usage: issue [-a] [-p owner/repo] <query>
//
// Issue runs the query against the given project's issue tracker and
// prints a table of matching issues, sorted by issue summary.
// The default owner/repo is golang/go.
//
// If multiple arguments are given as the query, issue joins them by
// spaces to form a single issue search. These two commands are equivalent:
//
//	issue assignee:rsc author:robpike
//	issue "assignee:rsc author:robpike"
//
// Searches are always limited to open issues.
//
// If the query is a single number, issue prints that issue in detail,
// including all comments.
//
// Acme
//
// If the -a flag is specified, issue runs as a collection of acme windows
// instead of a command-line tool. In this mode, the query is optional.
// If no query is given, issue uses "state:open".
//
// There are three kinds of acme windows: issue, issue creation, issue list,
// search result, and milestone list.
//
// The following text forms can be looked for (right clicked on)
// and open a window (or navigate to an existing one).
//
//	nnnn			issue #nnnn
//	#nnnn			issue #nnnn
//	all			the issue list
//	milestone(s)		the milestone list
//	<milestone-name>	the named milestone (e.g., Go1.5)
//
// Executing "New" opens an issue creation window.
//
// Executing "Search <query>" opens a new window showing the
// results of that search.
//
// Issue Window
//
// An issue window, opened by loading an issue number,
// displays full detail about an issue, a header followed by each comment.
// For example:
//
//	Title: time: Duration should implement fmt.Formatter
//	State: closed
//	Assignee: robpike
//	Closed: 2015-01-08 05:20:00
//	Labels: release-none repo-main size-m
//	Milestone:
//	URL: https://github.com/golang/go/issues/8786
//
//	Reported by dsymonds (2014-09-21 23:02:50)
//
//		It'd be nice if http://play.golang.org/p/KCnUQOPyol
//		printed "[+3us]", which would require time.Duration
//		implementing fmt.Formatter to get the '+' flag.
//
//	Comment by rsc (2015-01-08 05:17:06)
//
//		time must not depend on fmt.
//
// Executing "Get" reloads the issue data.
//
// Executing "Put" updates an issue. It saves any changes to the issue header
// and, if any text has been entered between the header and the "Reported by" line,
// posts that text as a new comment. If both succeed, Put then reloads the issue data.
// The "Closed" and "URL" headers cannot be changed.
//
// Issue Creation Window
//
// An issue creation window, opened by executing "New", is like an issue window
// but displays only an empty issue template:
//
//	Title:
//	Assignee:
//	Labels:
//	Milestone:
//
//	<describe issue here>
//
// Once the template has been completed, executing "Put" creates the issue and converts
// the window into a issue window for the new issue.
//
// Issue List Window
//
// An issue list window displays a list of all open issue numbers and titles.
// If the project has any open milestones, they are listed in a header line.
// For example:
//
//	Milestones: Go1.4.1 Go1.5 Go1.5Maybe
//
//	9027	archive/tar: round-trip of Header misses values
//	8669	archive/zip: not possible to a start writing zip at offset other than zero
//	8359	archive/zip: not possible to specify deflate compression level
//	...
//
// As in any window, right clicking on an issue number opens a window for that issue.
//
// Search Result Window
//
// A search result window, opened by executing "Search <query>", displays a list of issues
// matching a search query. It shows the query in a header line. For example:
//
//	Search: author:rsc
//
//	#9131	bench: no documentation
//	#599	cmd/5c, 5g, 8c, 8g: make 64-bit fields 64-bit aligned
//	#6699	cmd/5l: use m to store div/mod denominator
//	#4997	cmd/6a, cmd/8a: MOVL $x-8(SP) and LEAL x-8(SP) are different
//	...
//
// Milestone List Window
//
// The milestone list window, opened by loading any of the names
// "milestone", "Milestone", or "Milestones", displays the open project
// milestones, sorted by due date, along with the number of open issues in each.
// For example:
//
//	2015-01-15	Go1.4.1		1
//	2015-07-31	Go1.5		215
//	2015-07-31	Go1.5Maybe	5
//
// Loading one of the listed milestone names opens a search for issues
// in that milestone.
//
package main // import "rsc.io/github/issue"

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var (
	acmeFlag = flag.Bool("a", false, "acme")

	project      = flag.String("p", "golang/go", "GitHub owner/repo name")
	projectOwner = ""
	projectRepo  = ""
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: issue [-a] [-p owner/repo] <query>

If query is a single number, prints the full history for the issue.
Otherwise, prints a table of matching results.
`)
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("issue: ")

	if flag.NArg() == 0 && !*acmeFlag {
		usage()
	}
	q := strings.Join(flag.Args(), " ")

	f := strings.Split(*project, "/")
	if len(f) != 2 {
		log.Fatal("invalid form for -p argument: must be owner/repo, like golang/go")
	}
	projectOwner = f[0]
	projectRepo = f[1]

	loadAuth()

	if *acmeFlag {
		acmeMode()
	}

	n, _ := strconv.Atoi(q)
	if n != 0 {
		if _, err := showIssue(os.Stdout, n); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := showQuery(os.Stdout, q); err != nil {
		log.Fatal(err)
	}
}

func showIssue(w io.Writer, n int) (*github.Issue, error) {
	issue, _, err := client.Issues.Get(projectOwner, projectRepo, n)
	if err != nil {
		return nil, err
	}
	return issue, printIssue(w, issue)
}

const timeFormat = "2006-01-02 15:04:05"

func printIssue(w io.Writer, issue *github.Issue) error {
	fmt.Fprintf(w, "Title: %s\n", getString(issue.Title))
	fmt.Fprintf(w, "State: %s\n", getString(issue.State))
	fmt.Fprintf(w, "Assignee: %s\n", getUserLogin(issue.Assignee))
	if issue.ClosedAt != nil {
		fmt.Fprintf(w, "Closed: %s\n", getTime(issue.ClosedAt).Format(timeFormat))
	}
	fmt.Fprintf(w, "Labels: %s\n", strings.Join(getLabelNames(issue.Labels), " "))
	fmt.Fprintf(w, "Milestone: %s\n", getMilestoneTitle(issue.Milestone))
	fmt.Fprintf(w, "URL: https://github.com/%s/%s/issues/%d\n", projectOwner, projectRepo, getInt(issue.Number))

	fmt.Fprintf(w, "\nReported by %s (%s)\n", getUserLogin(issue.User), getTime(issue.CreatedAt).Format(timeFormat))
	if issue.Body != nil {
		text := strings.TrimSpace(*issue.Body)
		if text != "" {
			fmt.Fprintf(w, "\n\t%s\n", wrap(text, "\t"))
		}
	}

	for page := 1; ; {
		list, resp, err := client.Issues.ListComments(projectOwner, projectRepo, getInt(issue.Number), &github.IssueListCommentsOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
		})
		for _, com := range list {
			fmt.Fprintf(w, "\nComment by %s (%s)\n", getUserLogin(com.User), getTime(com.CreatedAt).Format(timeFormat))
			if com.Body != nil {
				text := strings.TrimSpace(*com.Body)
				if text != "" {
					fmt.Fprintf(w, "\n\t%s\n", wrap(text, "\t"))
				}
			}
		}
		if err != nil {
			return err
		}
		if resp.NextPage < page {
			break
		}
		page = resp.NextPage
	}
	return nil
}

func showQuery(w io.Writer, q string) error {
	var all []string
	for page := 1; ; {
		x, resp, err := client.Search.Issues("type:issue state:open repo:"+*project+" "+q, &github.SearchOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
		})
		if err != nil {
			return err
		}
		for _, issue := range x.Issues {
			all = append(all, fmt.Sprintf("%s\t%d", getString(issue.Title), getInt(issue.Number)))
		}
		if resp.NextPage < page {
			break
		}
		page = resp.NextPage
	}
	sort.Strings(all)
	for _, s := range all {
		i := strings.LastIndex(s, "\t")
		fmt.Fprintf(w, "%s\t%s\n", s[i+1:], s[:i])
	}
	return nil
}

func loadMilestones() ([]github.Milestone, error) {
	// NOTE(rsc): There appears to be no paging possible.
	all, _, err := client.Issues.ListMilestones(projectOwner, projectRepo, &github.MilestoneListOptions{
		State: "open",
	})
	if err != nil {
		return nil, err
	}
	if all == nil {
		all = []github.Milestone{}
	}
	return all, nil
}

func wrap(t string, prefix string) string {
	out := ""
	t = strings.Replace(t, "\r\n", "\n", -1)
	lines := strings.Split(t, "\n")
	for i, line := range lines {
		if i > 0 {
			out += "\n" + prefix
		}
		s := line
		for len(s) > 70 {
			i := strings.LastIndex(s[:70], " ")
			if i < 0 {
				i = 69
			}
			i++
			out += s[:i] + "\n" + prefix
			s = s[i:]
		}
		out += s
	}
	return out
}

var client *github.Client

// GitHub personal access token, from https://github.com/settings/applications.
var authToken string

func loadAuth() {
	const short = ".github-issue-token"
	filename := filepath.Clean(os.Getenv("HOME") + "/" + short)
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal("reading token: ", err, "\n\n"+
			"Please create a personal access token at https://github.com/settings/tokens/new\n"+
			"and write it to ", filepath.Clean("$HOME/"+short), " to use this program.\n"+
			"The token only needs the repo scope, or private_repo if you want to\n"+
			"view or edit issues for private repositories.\n"+
			"The benefit of using a personal access token over using your GitHub\n"+
			"password directly is that you can limit its use and revoke it at any time.\n\n")
	}
	fi, err := os.Stat(filename)
	if fi.Mode()&0077 != 0 {
		log.Fatalf("reading token: %s mode is %#o, want %#o", filepath.Clean("$HOME/"+short), fi.Mode()&0777, fi.Mode()&0700)
	}
	authToken = strings.TrimSpace(string(data))
	t := &oauth2.Transport{
		Source: &tokenSource{AccessToken: authToken},
	}
	client = github.NewClient(&http.Client{Transport: t})
}

type tokenSource oauth2.Token

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return (*oauth2.Token)(t), nil
}

func getInt(x *int) int {
	if x == nil {
		return 0
	}
	return *x
}

func getString(x *string) string {
	if x == nil {
		return ""
	}
	return *x
}

func getUserLogin(x *github.User) string {
	if x == nil || x.Login == nil {
		return ""
	}
	return *x.Login
}

func getTime(x *time.Time) time.Time {
	if x == nil {
		return time.Time{}
	}
	return *x
}

func getMilestoneTitle(x *github.Milestone) string {
	if x == nil || x.Title == nil {
		return ""
	}
	return *x.Title
}

func getLabelNames(x []github.Label) []string {
	var out []string
	for _, lab := range x {
		out = append(out, getString(lab.Name))
	}
	return out
}
