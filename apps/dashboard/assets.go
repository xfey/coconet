// Package dashboard embeds the browser application into the Server.
package dashboard

import "embed"

//go:embed assets/*.html assets/*.css assets/*.mjs assets/*.svg assets/fonts/* assets/vendor/*
var Assets embed.FS
