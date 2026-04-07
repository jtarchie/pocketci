package server_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func mustHTMLDocument(t *testing.T, rec *httptest.ResponseRecorder) *goquery.Document {
	t.Helper()

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rec.Body.String()))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	return doc
}

func mustJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var payload map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("decode json response: %v", err)
	}

	return payload
}

func mustSSEJSONEvents(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()

	lines := strings.Split(rec.Body.String(), "\n")
	events := make([]map[string]any, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		jsonPayload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if jsonPayload == "" {
			continue
		}

		var event map[string]any
		err := json.Unmarshal([]byte(jsonPayload), &event)
		if err != nil {
			t.Fatalf("decode sse data payload: %v", err)
		}
		events = append(events, event)
	}

	if len(events) == 0 {
		t.Fatalf("no JSON SSE events found in response")
	}

	return events
}

func mustJSONErrorText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	payload := mustJSONMap(t, rec)
	for _, key := range []string{"message", "error"} {
		value, ok := payload[key].(string)
		if ok {
			return value
		}
	}

	t.Fatalf("json error payload missing string message/error fields: %+v", payload)
	return ""
}

func hasSelectorWithText(doc *goquery.Document, selector string, text string) bool {
	return doc.Find(selector).FilterFunction(func(_ int, selection *goquery.Selection) bool {
		normalized := strings.Join(strings.Fields(selection.Text()), " ")
		return strings.Contains(normalized, text)
	}).Length() > 0
}

func selectorHasAttrValue(doc *goquery.Document, selector string, attr string, value string) bool {
	return doc.Find(selector).FilterFunction(func(_ int, selection *goquery.Selection) bool {
		actual, ok := selection.Attr(attr)
		return ok && actual == value
	}).Length() > 0
}

func selectorHasAttrContaining(doc *goquery.Document, selector string, attr string, value string) bool {
	return doc.Find(selector).FilterFunction(func(_ int, selection *goquery.Selection) bool {
		actual, ok := selection.Attr(attr)
		return ok && strings.Contains(actual, value)
	}).Length() > 0
}

func mustBeValidHTMLDocumentStrict(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "<!doctype html>") {
		t.Fatalf("missing <!DOCTYPE html> declaration")
	}

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse html document: %v", err)
	}

	doc := mustHTMLDocument(t, rec)
	htmlSel := doc.Find("html")
	if htmlSel.Length() != 1 {
		t.Fatalf("expected one <html> element, got %d", htmlSel.Length())
	}

	if lang, ok := htmlSel.Attr("lang"); !ok || strings.TrimSpace(lang) == "" {
		t.Fatalf("expected <html> to include non-empty lang attribute")
	}

	if doc.Find("head").Length() != 1 {
		t.Fatalf("expected one <head> element")
	}

	if doc.Find("body").Length() != 1 {
		t.Fatalf("expected one <body> element")
	}

	if strings.TrimSpace(doc.Find("head > title").First().Text()) == "" {
		t.Fatalf("expected non-empty <title>")
	}

	if doc.Find("main#main-content").Length() == 0 {
		t.Fatalf("expected main landmark with id=main-content")
	}

	if doc.Find("h1").Length() == 0 {
		t.Fatalf("expected at least one <h1>")
	}

	mustHaveUniqueIDs(t, root)
	mustHaveAccessibleInteractiveNames(t, doc)
}

func mustBeValidHTMLFragmentStrict(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	body := rec.Body.String()
	if strings.TrimSpace(body) == "" {
		t.Fatalf("expected non-empty HTML fragment")
	}

	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	fragments, err := html.ParseFragment(strings.NewReader(body), ctx)
	if err != nil {
		t.Fatalf("parse html fragment: %v", err)
	}

	if len(fragments) == 0 {
		t.Fatalf("expected parsed fragment nodes")
	}

	doc := mustHTMLDocument(t, rec)
	mustHaveAccessibleInteractiveNames(t, doc)

	seen := map[string]struct{}{}
	for _, fragment := range fragments {
		collectDuplicateIDs(t, fragment, seen)
	}
}

func mustHaveUniqueIDs(t *testing.T, root *html.Node) {
	t.Helper()

	seen := map[string]struct{}{}
	collectDuplicateIDs(t, root, seen)
}

func collectDuplicateIDs(t *testing.T, node *html.Node, seen map[string]struct{}) {
	t.Helper()

	if node.Type == html.ElementNode {
		for _, attr := range node.Attr {
			if attr.Key != "id" {
				continue
			}

			id := strings.TrimSpace(attr.Val)
			if id == "" {
				t.Fatalf("found empty id attribute")
			}

			if _, exists := seen[id]; exists {
				t.Fatalf("duplicate id attribute found: %s", id)
			}

			seen[id] = struct{}{}
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectDuplicateIDs(t, child, seen)
	}
}

func mustHaveAccessibleInteractiveNames(t *testing.T, doc *goquery.Document) {
	t.Helper()

	doc.Find("button, a, input, summary").Each(func(_ int, selection *goquery.Selection) {
		if selection.Is("input[type='hidden']") {
			return
		}

		if selection.Is("a") {
			href, ok := selection.Attr("href")
			if !ok || strings.TrimSpace(href) == "" {
				t.Fatalf("anchor is missing non-empty href")
			}
		}

		if hasAccessibleName(selection) {
			return
		}

		t.Fatalf("interactive element is missing an accessible name: <%s>", goquery.NodeName(selection))
	})
}

func hasAccessibleName(selection *goquery.Selection) bool {
	for _, attr := range []string{"aria-label", "aria-labelledby", "title", "value", "placeholder"} {
		value, ok := selection.Attr(attr)
		if ok && strings.TrimSpace(value) != "" {
			return true
		}
	}

	return strings.TrimSpace(selection.Text()) != ""
}
