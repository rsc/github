// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"
	"rsc.io/oauthprompt"
)

func getClient() *http.Client {
	tokFile := "/Users/rsc/.cred/minutes2.json"
	client, err := oauthprompt.GoogleToken(tokFile, "512347153416-eehjrr21snt0av7n1opjsorg889fcged.apps.googleusercontent.com", "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", "https://www.googleapis.com/auth/documents")
	if err != nil {
		log.Fatal(err)
	}
	return client
}

type Doc struct {
	Text   []string // top-level text
	Who    []string
	Issues []*Issue
}

type Issue struct {
	Number  int
	Title   string
	Details string
	Minutes string
	Comment string
	Notes   string
}

func parseDoc() *Doc {
	var doc *docs.Document
	if true {
		client := getClient()

		srv, err := docs.NewService(ctx, option.WithHTTPClient(client))
		if err != nil {
			log.Fatalf("Unable to retrieve Docs client: %v", err)
		}

		docId := "1Ri8QwTL6Scwm1Ke1cd1gIZIYwBffViuOCIRJDYARZU8"

		/*
			resp, err := srv.Documents.BatchUpdate(docId, &docs.BatchUpdateDocumentRequest{
				Requests: []*docs.Request{
					{
						InsertText: &docs.InsertTextRequest{
							Location: &docs.Location{
								Index: 1,
							},
							Text: "A",
						},
					},
					{
						InsertText: &docs.InsertTextRequest{
							Location: &docs.Location{
								Index: 2,
							},
							Text: "B",
						},
					},
				},
			}).Do()
			if err != nil {
				log.Fatal(err)
			}
			js, err := json.Marshal(resp)
			js = append(js, '\n')
			os.Stdout.Write(js)
			return nil
		*/

		doc, err = srv.Documents.Get(docId).Do()
		if err != nil {
			log.Fatalf("Unable to retrieve data from document: %v", err)
		}
	} else {
		doc = new(docs.Document)
		data, err := os.ReadFile("x.json")
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(data, doc); err != nil {
			log.Fatal(err)
		}
	}

	d := new(Doc)
	top := ""
	for _, elem := range doc.Body.Content {
		if para := elem.Paragraph; para != nil {
			content := ""
			for _, elem := range para.Elements {
				if run := elem.TextRun; run != nil {
					content += run.Content
				}
			}
			top += strings.Trim(strings.ReplaceAll(content, "\v", "\n"), "\n") + "\n"
		}
		if table := elem.Table; table != nil {
			rest, line := cutLastLine(top)
			if strings.HasPrefix(line, "#NNNNN") {
				continue
			}
			if !strings.HasPrefix(line, "#") {
				log.Fatalf("bad issue: %s", line)
			}
			num, title, ok := strings.Cut(line, " ")
			if !ok {
				log.Fatalf("bad issue2: %s", line)
			}
			n, err := strconv.Atoi(strings.TrimPrefix(num, "#"))
			if err != nil {
				log.Fatalf("bad issue3: %s", line)
			}
			issue := &Issue{
				Number: n,
				Title:  title,
			}
			d.Issues = append(d.Issues, issue)
			top = rest
			for _, row := range table.TableRows {
				for _, cell := range row.TableCells {
					content := ""
					for _, elem := range cell.Content {
						if para := elem.Paragraph; para != nil {
							for _, elem := range para.Elements {
								if run := elem.TextRun; run != nil {
									content += run.Content
								}
							}
						}
					}
					content = strings.ReplaceAll(content, "\v", "\n")
					if strings.HasPrefix(content, "Minutes:") {
						issue.Minutes = strings.TrimSpace(strings.TrimPrefix(content, "Minutes:"))
						continue
					}
					first, rest, _ := strings.Cut(content, "\n")
					if !strings.HasSuffix(first, ":") {
						log.Fatalf("missing colon: %s", content)
					}
					rest = strings.Trim(rest, "\n")
					if rest != "" {
						rest += "\n"
					}
					if rest == "None\n" || rest == "TBD\n" {
						rest = ""
					}
					switch {
					case strings.HasPrefix(first, "Proposal details"):
						issue.Details = rest
					case strings.HasPrefix(first, "Comment"):
						issue.Comment = rest
					case strings.HasPrefix(first, "Private notes"), strings.HasPrefix(first, "Discussion notes"):
						issue.Notes = rest
					default:
						log.Fatalf("unknown cell: %s", content)
					}
				}
			}
		}
	}
	for _, line := range strings.Split(top, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Attendees:") {
			d.Who = strings.Fields(strings.TrimPrefix(line, "Attendees:"))
			for i, a := range d.Who {
				d.Who[i] = strings.Trim(a, ",")
			}
		}
		d.Text = append(d.Text, line)
	}

	return d
	/*
		content := doc.Body.Content


		js, err := json.MarshalIndent(doc, "", "\t")
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(append(js, '\n'))
	*/
}

func cutLastLine(s string) (rest, line string) {
	s = strings.TrimRight(s, "\n")
	i := strings.LastIndex(s, "\n")
	return s[:i+1], s[i+1:]
}

/*
func main() {
	doc := parseDoc()
	js, err := json.MarshalIndent(doc, "", "\t")
	if err != nil {
		log.Fatal(err)
	}
	os.Stdout.Write(append(js, '\n'))
}
*/
