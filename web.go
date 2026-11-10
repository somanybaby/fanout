package main

import (
	_ "embed"
	"net/http"
	"strings"
)

// All dashboard resources remain embedded, so installation needs no asset server.
//
//go:embed ui/index.html
var dashboardLayout string

//go:embed ui/base.css
var dashboardBaseCSS string

//go:embed ui/dashboard.css
var dashboardCSS string

//go:embed ui/modals.html
var dashboardModals string

//go:embed ui/legacy.js
var dashboardLegacyJS string

//go:embed ui/dashboard.js
var dashboardJS string

func dashboardHTML() string {
	return strings.NewReplacer(
		"<!--STYLES-->", "<style>"+dashboardBaseCSS+"\n"+dashboardCSS+"</style>",
		"<!--MODALS-->", dashboardModals,
		"<!--SCRIPTS-->", "<script>"+dashboardLegacyJS+"</script>"+selectionHTML+"<script>"+dashboardJS+"</script>",
	).Replace(dashboardLayout)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(dashboardHTML()))
}
