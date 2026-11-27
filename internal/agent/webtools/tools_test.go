package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchThenFetch(t *testing.T) {
	const page = "https://example.com/article"
	content := strings.Repeat("界", maxPageSize)
	calls := 0
	c := client{apiKey: "test-key", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("incorrect method or headers")
		}
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var response string
		switch calls {
		case 1:
			if r.URL.String() != deepSeekToolsURL+"search" || string(data) != `{"query":"搜索词","count":5,"need_content":false}` {
				t.Fatalf("search request %s %s", r.URL, data)
			}
			response = fmt.Sprintf(`{"results":[{"url":"javascript:alert(1)"},{"title":"文章","url":%q,"summary":"摘要","date":"2026-11-27T00:00:00Z","content":"omit"}]}`, page)
		case 2:
			if r.URL.String() != deepSeekToolsURL+"fetch" || string(data) != `{"url":"`+page+`"}` {
				t.Fatalf("fetch request %s %s", r.URL, data)
			}
			response = fmt.Sprintf(`{"title":"文章","content":%q}`, content)
		default:
			t.Fatal("unexpected provider call")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}}
	tools := c.tools()
	result, err := tools[0].Run(t.Context(), fantasy.ToolCall{Input: `{"query":" 搜索词 "}`})
	var search searchOutput
	if err != nil || result.IsError || json.Unmarshal([]byte(result.Content), &search) != nil || len(search.Results) != 1 ||
		search.Results[0].URL != page || search.Results[0].Summary != "摘要" || strings.Contains(result.Content, "omit") {
		t.Fatalf("search result = %+v, %v", result, err)
	}
	input, err := json.Marshal(fetchInput{URL: search.Results[0].URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err = tools[1].Run(t.Context(), fantasy.ToolCall{Input: string(input)})
	var fetch fetchOutput
	if err != nil || result.IsError || json.Unmarshal([]byte(result.Content), &fetch) != nil || !fetch.Truncated ||
		fetch.URL != page || !utf8.ValidString(fetch.Content) || len(fetch.Content) > maxPageSize || !strings.HasPrefix(content, fetch.Content) || calls != 2 {
		t.Fatalf("invalid fetch result: length=%d, truncated=%v, calls=%d, err=%v", len(fetch.Content), fetch.Truncated, calls, err)
	}
}

func TestWebToolsRejectInvalidArgumentsBeforeRequest(t *testing.T) {
	c := client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid arguments reached the network")
		return nil, nil
	})}}
	tools := c.tools()
	for _, tc := range []struct {
		tool  int
		input string
	}{
		{0, `{}`},
		{0, `{"query":"   "}`},
		{0, `{"query":"` + strings.Repeat("界", 129) + `"}`},
		{0, `{"query":"topic","count":50}`},
		{1, `{"url":"file:///tmp/x"}`},
		{1, `{"url":"https://user:password@example.com/"}`},
		{1, `{"url":"https:///x"}`},
		{1, `{"url":"https://example.com/` + strings.Repeat("x", maxURLSize) + `"}`},
	} {
		out, err := tools[tc.tool].Run(t.Context(), fantasy.ToolCall{Input: tc.input})
		if err != nil || !out.IsError {
			t.Fatalf("tool %d accepted invalid arguments: %+v, %v", tc.tool, out, err)
		}
	}
}

func TestWebToolResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		tool   int
		want   string
	}{
		{"empty search", 200, `{"results":[]}`, 0, ""},
		{"empty page", 200, `{"content":" "}`, 1, "could not retrieve"},
		{"invalid JSON", 200, `{"results":[]}{`, 0, "invalid web tool response"},
		{"missing results", 200, `{}`, 0, "invalid web tool response"},
		{"oversized", 200, strings.Repeat("x", maxResponseSize+1), 0, "size limit"},
		{"bad key", 401, "sensitive upstream error", 0, "API key is invalid"},
		{"balance", 402, "", 1, "insufficient balance"},
		{"page denied", 403, "", 1, "denied"},
		{"page unavailable", 404, "", 1, "could not retrieve"},
		{"rate limit", 429, "", 0, "rate limit"},
		{"server error", 500, "", 0, "unavailable"},
		{"timeout", 504, "", 1, "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			input := []string{`{"query":"topic"}`, `{"url":"https://example.com"}`}[tc.tool]
			out, err := c.tools()[tc.tool].Run(t.Context(), fantasy.ToolCall{Input: input})
			if err != nil || out.IsError != (tc.want != "") || !strings.Contains(out.Content, tc.want) || strings.Contains(out.Content, "sensitive upstream error") {
				t.Fatalf("output=%+v, err=%v", out, err)
			}
			if tc.want == "" && out.Content != `{"results":[]}` {
				t.Fatalf("empty search output=%q", out.Content)
			}
		})
	}
}

func TestWebToolRequestFollowsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	})}}
	out, err := c.tools()[0].Run(ctx, fantasy.ToolCall{Input: `{"query":"topic"}`})
	if !errors.Is(err, context.Canceled) || out.Content != "" {
		t.Fatalf("canceled output=%+v, err=%v", out, err)
	}
}
