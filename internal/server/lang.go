package server

import (
	"net/http"
	"strings"
)

// lang is the language of text the server generates for the web UI, such as
// restore guides and assistant errors. The web UI sends its language in
// Accept-Language.
type lang int

const (
	langZH lang = iota
	langEN
)

// requestLang returns English when the most preferred language of r is
// English, and Chinese otherwise.
func requestLang(r *http.Request) lang {
	first, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	tag, _, _ := strings.Cut(first, ";")
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "en" || strings.HasPrefix(tag, "en-") {
		return langEN
	}
	return langZH
}

// pick returns zh or en for l.
func (l lang) pick(zh, en string) string {
	if l == langEN {
		return en
	}
	return zh
}
