// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// API documentation viewer (FR-060, UI-SPEC §5.12): the in-house renderer
// of the embedded OpenAPI document — server-side YAML parsing, semantic
// HTML, accessible without JavaScript. Swagger UI and Redoc were rejected
// (megabytes of unauditable JS, ADR-0010/NFR-019). The chrome is
// bilingual; the contract itself stays in English, marked lang="en". Curl
// examples are contextualized on the request host through the copiable
// chip component.

package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
)

// oapiDoc is the minimal projection of the OpenAPI 3.1 document the
// viewer needs: paths, methods, summaries, parameters.
type oapiDoc struct {
	Paths map[string]map[string]oapiOperation `yaml:"paths"`
}

// oapiOperation is one method of one path.
type oapiOperation struct {
	Summary    string      `yaml:"summary"`
	Parameters []oapiParam `yaml:"parameters"`
}

// oapiParam is one declared parameter. $ref entries resolve against the
// components.parameters section at load time.
type oapiParam struct {
	Ref         string `yaml:"$ref"`
	Name        string `yaml:"name"`
	In          string `yaml:"in"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

// oapiComponents resolves the shared parameter definitions.
type oapiComponents struct {
	Components struct {
		Parameters map[string]oapiParam `yaml:"parameters"`
	} `yaml:"components"`
}

// apiEndpoint is one rendered operation of the viewer.
type apiEndpoint struct {
	Method  string
	Path    string
	Summary string
	Params  []oapiParam
}

// apiEndpoints projects the embedded OpenAPI document once at startup; a
// parse failure is a build defect (the document ships in the binary).
var apiEndpoints = func() []apiEndpoint {
	raw := api.OpenAPI()
	var doc oapiDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		panic(fmt.Sprintf("ui: parsing embedded OpenAPI document: %v", err))
	}
	var comps oapiComponents
	if err := yaml.Unmarshal(raw, &comps); err != nil {
		panic(fmt.Sprintf("ui: parsing OpenAPI components: %v", err))
	}

	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []apiEndpoint
	for _, p := range paths {
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			op, ok := doc.Paths[p][method]
			if !ok {
				continue
			}
			ep := apiEndpoint{
				Method:  strings.ToUpper(method),
				Path:    p,
				Summary: strings.TrimSpace(op.Summary),
			}
			for _, param := range op.Parameters {
				if param.Ref != "" {
					name := param.Ref[strings.LastIndexByte(param.Ref, '/')+1:]
					resolved, found := comps.Components.Parameters[name]
					if !found {
						panic(fmt.Sprintf("ui: OpenAPI parameter reference %q unresolved", param.Ref))
					}
					param = resolved
				}
				ep.Params = append(ep.Params, param)
			}
			out = append(out, ep)
		}
	}
	return out
}()

// apidocsRow decorates one endpoint with its host-contextualized curl
// example (UI-SPEC §5.12).
type apidocsRow struct {
	apiEndpoint
	Curl string
}

// apidocsData feeds the /api-docs page.
type apidocsData struct {
	Endpoints []apidocsRow
}

// apiDocsScreen serves GET /api-docs for every signed-in role.
func (u *UI) apiDocsScreen(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	data := &apidocsData{Endpoints: make([]apidocsRow, 0, len(apiEndpoints))}
	for _, ep := range apiEndpoints {
		curl := "curl -u account:password"
		if ep.Method != http.MethodGet {
			curl += " -X " + ep.Method
		}
		curl += " " + scheme + "://" + r.Host + ep.Path
		data.Endpoints = append(data.Endpoints, apidocsRow{apiEndpoint: ep, Curl: curl})
	}
	u.render.Page(w, r, "api-docs", data)
}
