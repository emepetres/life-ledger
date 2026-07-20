// Package web holds the server-rendered templates and static assets, embedded
// into the binary so the whole app ships as a single deployable unit.
package web

import "embed"

// Templates holds the html/template sources rendered by the server.
//
//go:embed templates
var Templates embed.FS

// Static holds assets served verbatim under /static/ (e.g. the vendored,
// version-pinned htmx script). No CDN is used — every asset ships in the binary.
//
//go:embed static
var Static embed.FS
