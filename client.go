package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the tududi REST API with an API key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Task status values as stored by tududi.
var statusNames = []string{"not_started", "in_progress", "done", "archived", "waiting", "cancelled", "planned"}

const statusDone = 2

var priorityNames = []string{"low", "medium", "high"}

type Tag struct {
	Name string `json:"name"`
}

type Project struct {
	ID   int    `json:"id"`
	UID  string `json:"uid"`
	Name string `json:"name"`
}

type Task struct {
	UID          string   `json:"uid"`
	Name         string   `json:"name"`
	OriginalName string   `json:"original_name"`
	Note         string   `json:"note"`
	Status       int      `json:"status"`
	Priority     int      `json:"priority"`
	DueDate      string   `json:"due_date"`
	Tags         []Tag    `json:"tags"`
	Project      *Project `json:"Project"`
}

// DisplayName is the name the user typed. tududi replaces the name of a
// recurring template with its cadence ("Daily"), so original_name wins.
func (t Task) DisplayName() string {
	if t.OriginalName != "" {
		return t.OriginalName
	}
	return t.Name
}

func (t Task) StatusName() string {
	if t.Status >= 0 && t.Status < len(statusNames) {
		return statusNames[t.Status]
	}
	return fmt.Sprint(t.Status)
}

func (t Task) PriorityName() string {
	if t.Priority >= 0 && t.Priority < len(priorityNames) {
		return priorityNames[t.Priority]
	}
	return fmt.Sprint(t.Priority)
}

// Analysis is what tududi reads from quick-add text: #tags, +project and dates.
type Analysis struct {
	CleanedContent string   `json:"cleaned_content"`
	ParsedTags     []string `json:"parsed_tags"`
	ParsedProjects []string `json:"parsed_projects"`
	ParsedDueDate  *string  `json:"parsed_due_date"`
}

// APIError is a non-2xx response from tududi.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &body) == nil && body.Error != "" {
		return fmt.Sprintf("tududi: %d: %s", e.Status, body.Error)
	}
	return fmt.Sprintf("tududi: %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func (c *Client) do(method, path string, query url.Values, in, out any) error {
	u := c.BaseURL + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) Analyze(text string) (Analysis, error) {
	var a Analysis
	err := c.do(http.MethodPost, "/inbox/analyze-text", nil, map[string]any{"content": text}, &a)
	return a, err
}

// ListTasks returns the list response as tududi sent it, so --json output
// keeps every field. DecodeTasks reads the tasks out of it.
func (c *Client) ListTasks(query url.Values) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(http.MethodGet, "/tasks", query, nil, &raw)
	return raw, err
}

func DecodeTasks(raw json.RawMessage) ([]Task, error) {
	var resp struct {
		Tasks []Task `json:"tasks"`
	}
	err := json.Unmarshal(raw, &resp)
	return resp.Tasks, err
}

func (c *Client) GetTask(uid string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(http.MethodGet, "/task/"+url.PathEscape(uid), nil, nil, &raw)
	return raw, err
}

func (c *Client) CreateTask(body map[string]any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(http.MethodPost, "/task", nil, body, &raw)
	return raw, err
}

func (c *Client) UpdateTask(uid string, body map[string]any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(http.MethodPatch, "/task/"+url.PathEscape(uid), nil, body, &raw)
	return raw, err
}

func (c *Client) DeleteTask(uid string) error {
	return c.do(http.MethodDelete, "/task/"+url.PathEscape(uid), nil, nil, nil)
}

func (c *Client) Projects() ([]Project, error) {
	var resp struct {
		Projects []Project `json:"projects"`
	}
	err := c.do(http.MethodGet, "/projects", nil, nil, &resp)
	return resp.Projects, err
}

// FindProject resolves a project name the way the web app does: an exact,
// case-insensitive match.
func (c *Client) FindProject(name string) (*Project, error) {
	projects, err := c.Projects()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if strings.EqualFold(projects[i].Name, name) {
			return &projects[i], nil
		}
	}
	return nil, fmt.Errorf("no project named %q", name)
}
