// Package web contains PARADOX's dependency-free dashboard. The files are
// embedded so a single Go binary can serve the UI without a Node build step.
package web

import "embed"

//go:embed index.html app.js style.css
var FS embed.FS
