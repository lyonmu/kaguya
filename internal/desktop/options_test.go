package desktop

import (
	"context"
	"net/http"
	"testing"

	"github.com/lyonmu/kaguya/pkg"
)

func TestOptionsValidate(t *testing.T) {
	valid := Options{
		Name: "Kaguya", WindowTitle: "Kaguya", APIPrefix: "/kaguya/api",
		RootContext: context.Background(), Admission: pkg.NewAdmission(),
		Start:    func() (http.Handler, error) { return http.NotFoundHandler(), nil },
		Shutdown: func() {},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Options){
		"missing name":     func(o *Options) { o.Name = "" },
		"missing title":    func(o *Options) { o.WindowTitle = "" },
		"bad prefix":       func(o *Options) { o.APIPrefix = "/wails" },
		"missing context":  func(o *Options) { o.RootContext = nil },
		"missing gate":     func(o *Options) { o.Admission = nil },
		"missing start":    func(o *Options) { o.Start = nil },
		"missing shutdown": func(o *Options) { o.Shutdown = nil },
	} {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if err := opts.Validate(); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestValidateAPIPrefix(t *testing.T) {
	for _, prefix := range []string{"/kaguya/api", "/api", "/a/b/c"} {
		if err := validateAPIPrefix(prefix); err != nil {
			t.Fatalf("rejected %q: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"", "kaguya/api", "/", "/kaguya/", "/a//b", "/a/../b", "/a/..b", "/a?x=1", "/a#b", "/a\\b", "/wails", "/wails/hook", "/__desktop", "/assets", "/assets/x", "/kaguya-favicon.webp", "/kaguya-favicon.webp/x"} {
		if err := validateAPIPrefix(prefix); err == nil {
			t.Fatalf("accepted %q", prefix)
		}
	}
}

func TestAPIPrefixFragment(t *testing.T) {
	if got := apiPrefixFragment("/kaguya/api"); got != "api-prefix=%2Fkaguya%2Fapi" {
		t.Fatalf("fragment=%q", got)
	}
}
