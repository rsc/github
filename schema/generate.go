// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// go run generate.go downloads the latest GraphQL schema from GitHub
// and generates corresponding Go data structures in schema.go.
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"rsc.io/github/internal/graphql"
	"rsc.io/tmplfunc"
)

func main() {
	log.SetFlags(log.Lshortfile)
	data, err := os.ReadFile("schema.js")
	if err != nil {
		c, err := graphql.Dial()
		if err != nil {
			log.Fatal(err)
		}

		type schema struct {
			Types []Type `json:"types"`
		}
		var reply any
		err = c.GraphQL("schema", nil, &reply)
		if err != nil {
			log.Fatal(err)
		}
		js, err := json.MarshalIndent(reply, "", "\t")
		if err != nil {
			log.Fatal(err)
		}
		js = append(js, '\n')
		if err := os.WriteFile("schema.js", js, 0666); err != nil {
			log.Fatal(err)
		}
		data = js
	}
	var x struct {
		Schema *Schema `json:"__schema"`
	}
	if err := json.Unmarshal(data, &x); err != nil {
		log.Fatal(err)
	}

	tmpl := template.New("")
	tmpl.Funcs(template.FuncMap{
		"registerType": registerType,
		"link":         link,
		"strings":      func() stringsPkg { return stringsPkg{} },
		"upper":        upper,
	})
	if err := tmplfunc.ParseFiles(tmpl, "schema.tmpl"); err != nil {
		log.Fatal(err)
	}
	var b bytes.Buffer
	if err := tmpl.ExecuteTemplate(&b, "main", x.Schema); err != nil {
		log.Fatal(err)
	}

	if err := os.WriteFile("schema.go", b.Bytes(), 0666); err != nil {
		log.Fatal(err)
	}
	out, err := exec.Command("gofmt", "-w", "schema.go").CombinedOutput()
	if err != nil {
		log.Fatalf("gofmt schema.go: %v\n%s", err, out)
	}
}

type stringsPkg struct{}

func (stringsPkg) ReplaceAll(s, old, new string) string {
	return strings.ReplaceAll(s, old, new)
}

func (stringsPkg) TrimSuffix(s, suffix string) string {
	return strings.TrimSuffix(s, suffix)
}

var types []string

func registerType(name string) string {
	types = append(types, name)
	return ""
}

func upper(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

var docReplacer *strings.Replacer

func link(text string) string {
	if docReplacer == nil {
		sort.Strings(types)
		sort.SliceStable(types, func(i, j int) bool {
			return len(types[i]) > len(types[j])
		})
		var args []string
		for _, typ := range types {
			args = append(args, typ, "["+typ+"]")
		}
		docReplacer = strings.NewReplacer(args...)
	}
	return docReplacer.Replace(text)
}

type Directive struct {
	Name        string               `json:"name"`
	Args        []*InputValue        `json:"args"`
	Description string               `json:"description,omitempty"`
	Locations   []*DirectiveLocation `json:"locations,omitempty"`
}

type DirectiveLocation string // an enum

type EnumValue struct {
	Name              string `json:"name,omitempty"`
	Description       string `json:"description,omitempty"`
	DeprecationReason string `json:"deprecationReason,omitempty"`
	IsDeprecated      bool   `json:"isDeprecated,omitempty"`
}

type Field struct {
	Name              string        `json:"name,omitempty"`
	Description       string        `json:"description,omitempty"`
	Args              []*InputValue `json:"args,omitempty"`
	DeprecationReason string        `json:"deprecationReason,omitempty"`
	IsDeprecated      bool          `json:"isDeprecated,omitempty"`
	Type              *ShortType    `json:"type,omitempty"`
}

type InputValue struct {
	Name              string     `json:"name,omitempty"`
	Description       string     `json:"description,omitempty"`
	DefaultValue      any        `json:"defaultValue,omitempty"`
	DeprecationReason string     `json:"deprecationReason,omitempty"`
	IsDeprecated      bool       `json:"isDeprecated,omitempty"`
	Type              *ShortType `json:"type,omitempty"`
}

type Schema struct {
	Directives       []*Directive `json:"directives,omitempty"`
	MutationType     *ShortType   `json:"mutationType,omitempty"`
	QueryType        *ShortType   `json:"queryType,omitempty"`
	SubscriptionType *ShortType   `json:"subscriptionType,omitempty"`
	Types            []*Type      `json:"types,omitempty"`
}

type Type struct {
	Name          string        `json:"name,omitempty"`
	Description   string        `json:"description,omitempty"`
	EnumValues    []*EnumValue  `json:"enumValues,omitempty"`
	Fields        []*Field      `json:"fields,omitempty"`
	InputFields   []*InputValue `json:"inputFields,omitempty"`
	Interfaces    []*ShortType  `json:"interfaces,omitempty"`
	Kind          string        `json:"kind,omitempty"`
	OfType        *ShortType    `json:"ofType,omitempty"`
	PossibleTypes []*ShortType  `json:"possibleTypes,omitempty"`
}

type TypeKind string // an enum

type ShortType struct {
	Name   string     `json:"name,omitempty"`
	Kind   string     `json:"kind,omitempty"`
	OfType *ShortType `json:"ofType,omitempty"`
}

const query = `
query {
  __schema {
    directives {
      args ` + inputValue + `
      description
      name
      locations
    }
    mutationType ` + shortType + `
    queryType ` + shortType + `
    subscriptionType ` + shortType + `
    types {
      description
      enumValues {
        deprecationReason
        description
        isDeprecated
        name
      }
      fields {
        args ` + inputValue + `
        deprecationReason
        description
        isDeprecated
        name
        type ` + shortType + `
      }
      inputFields ` + inputValue + `
      interfaces ` + shortType + `
      kind
      name
      ofType ` + shortType + `
      possibleTypes ` + shortType + `
    }
  }
}
`

const inputValue = `
{
  defaultValue
  deprecationReason
  isDeprecated
  description
  name
  type ` + shortType + `
}
`

const shortType = `
{
  name
  kind
  ofType {
    name
    kind
    ofType {
      name
      kind
      ofType {
        name
        kind
      }
    }
  }
}
`
