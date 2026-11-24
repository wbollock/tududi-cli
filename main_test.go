package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type request struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

// server answers like tududi for the routes the CLI calls and records every
// request it gets.
func server(t *testing.T, analysis string) (*Client, *[]request) {
	t.Helper()
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tt_test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		req := request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &req.Body); err != nil {
				t.Fatalf("request body: %v", err)
			}
		}
		got = append(got, req)
		switch r.URL.Path {
		case "/api/v1/inbox/analyze-text":
			io.WriteString(w, analysis)
		case "/api/v1/projects":
			io.WriteString(w, `{"projects":[{"id":3,"uid":"proj1","name":"Infra"}]}`)
		case "/api/v1/task":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"uid":"task1","name":"fix router","original_name":"fix router"}`)
		case "/api/v1/tasks":
			io.WriteString(w, `{"tasks":[{"uid":"task1","name":"Daily","original_name":"water plants","due_date":"2026-11-27T00:00:00.000Z","Project":{"name":"Infra"},"tags":[{"name":"m"}]}]}`)
		case "/api/v1/task/task1":
			io.WriteString(w, `{"uid":"task1"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"Not found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL+"/", "tt_test"), &got
}

func TestAddMapsAnalysisToTask(t *testing.T) {
	c, got := server(t, `{"cleaned_content":"fix router","parsed_tags":["homelab","m"],"parsed_projects":["infra"],"parsed_due_date":"2026-11-25T00:00:00.000Z"}`)
	var out bytes.Buffer
	if err := cmdAdd(c, []string{"fix", "router", "#homelab", "#m", "+infra", "tomorrow"}, &out); err != nil {
		t.Fatal(err)
	}

	if analyzed := (*got)[0].Body["content"]; analyzed != "fix router #homelab #m +infra tomorrow" {
		t.Errorf("analyzed %q", analyzed)
	}
	create := (*got)[len(*got)-1]
	if create.Method != http.MethodPost || create.Path != "/api/v1/task" {
		t.Fatalf("last request %s %s", create.Method, create.Path)
	}
	want := `{"due_date":"2026-11-25","name":"fix router","project_uid":"proj1","tags":[{"name":"homelab"},{"name":"m"}]}`
	if b, _ := json.Marshal(create.Body); string(b) != want {
		t.Errorf("create body\n got %s\nwant %s", b, want)
	}
	if out.String() != "added task1 fix router\n" {
		t.Errorf("output %q", out.String())
	}
}

func TestAddUnknownProjectCreatesNothing(t *testing.T) {
	c, got := server(t, `{"cleaned_content":"x","parsed_projects":["nope"]}`)
	err := cmdAdd(c, []string{"x", "+nope"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `no project named "nope"`) {
		t.Fatalf("err = %v", err)
	}
	for _, r := range *got {
		if r.Path == "/api/v1/task" {
			t.Fatal("task was created")
		}
	}
}

func TestListQueryAndOutput(t *testing.T) {
	c, got := server(t, "")
	var out bytes.Buffer
	if err := cmdList(c, []string{"--project", "INFRA", "--tag", "m", "--today"}, &out); err != nil {
		t.Fatal(err)
	}
	list := (*got)[len(*got)-1]
	if list.Query != "project_uid=proj1&status=active&tags=m&type=today" {
		t.Errorf("query %q", list.Query)
	}
	if line := strings.Join(strings.Fields(out.String()), " "); line != "task1 2026-11-27 water plants +Infra #m" {
		t.Errorf("output %q", out.String())
	}
}

func TestEditSendsOnlyChangedFields(t *testing.T) {
	c, got := server(t, "")
	if err := cmdEdit(c, []string{"task1", "--due", "none", "--priority", "high", "--tags", "a, b,"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := `{"due_date":null,"priority":"high","tags":[{"name":"a"},{"name":"b"}]}`
	if b, _ := json.Marshal((*got)[0].Body); string(b) != want {
		t.Errorf("patch body\n got %s\nwant %s", b, want)
	}
}

func TestEditRejectsUnknownPriority(t *testing.T) {
	c, got := server(t, "")
	err := cmdEdit(c, []string{"task1", "--priority", "urgent"}, io.Discard)
	if err == nil || err.Error() != "priority must be one of low, medium, high" {
		t.Fatalf("err = %v", err)
	}
	if len(*got) != 0 {
		t.Fatal("request was sent")
	}
}

func TestAPIErrorShowsServerMessage(t *testing.T) {
	c, _ := server(t, "")
	_, err := c.GetTask("missing")
	if err == nil || err.Error() != "tududi: 404: Not found" {
		t.Fatalf("err = %v", err)
	}
}
