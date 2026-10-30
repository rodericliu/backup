package server

import (
	"net/http/httptest"
	"testing"
)

func TestRequestLang(t *testing.T) {
	for header, want := range map[string]lang{
		"":                           langZH,
		"zh-CN":                      langZH,
		"zh-CN,zh;q=0.9,en;q=0.8":    langZH,
		"en":                         langEN,
		"en-US,en;q=0.9,zh-CN;q=0.8": langEN,
		"EN-gb":                      langEN,
		" en ; q=1":                  langEN,
		"fr-FR,en;q=0.5":             langZH,
		"eng":                        langZH,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			r.Header.Set("Accept-Language", header)
		}
		if got := requestLang(r); got != want {
			t.Errorf("requestLang(%q) = %v, want %v", header, got, want)
		}
	}
}
