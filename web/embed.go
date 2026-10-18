// Package web embeds the chat widget served by the API at /widget.js.
package web

import _ "embed"

// WidgetJS is the chat widget script.
//
//go:embed widget.js
var WidgetJS []byte

// DemoHTML is the page served at /demo: the widget talking to this server.
//
//go:embed demo.html
var DemoHTML []byte
