package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"
)

// Set by goreleaser.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

const usage = `tududi-cli: manage tududi tasks from the terminal.

usage: tududi-cli <command> [flags] [args]

commands:
  add <text...>      Add a task. Reads #tags, +project and dates like the web app.
  ls                 List open tasks.
  show <uid>         Show one task.
  done <uid...>      Mark tasks done.
  edit <uid>         Change a task.
  rm <uid...>        Delete tasks.
  pick               Choose a task with fzf, then act on it.
  plan               Show today's Plan-your-day list.
  plan add <uid...>  Add tasks to today's plan without a time slot.
  version            Print the version.

environment:
  TUDUDI_URL         Server URL, for example https://tududi.example.com
  TUDUDI_API_KEY     API key from Profile > API keys

Run "tududi-cli <command> -h" for the flags of a command.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	if cmd == "version" {
		fmt.Fprintf(out, "tududi-cli %s (%s, %s)\n", Version, Commit, BuildDate)
		return nil
	}

	commands := map[string]func(*Client, []string, io.Writer) error{
		"add":  cmdAdd,
		"ls":   cmdList,
		"show": cmdShow,
		"done": cmdDone,
		"edit": cmdEdit,
		"rm":   cmdRemove,
		"pick": cmdPick,
		"plan": cmdPlan,
	}
	fn, ok := commands[cmd]
	if !ok {
		return fmt.Errorf("unknown command %q, run tududi-cli -h", cmd)
	}
	c, err := clientFromEnv()
	if err != nil {
		return err
	}
	return fn(c, args, out)
}

func clientFromEnv() (*Client, error) {
	base := os.Getenv("TUDUDI_URL")
	key := os.Getenv("TUDUDI_API_KEY")
	if base == "" || key == "" {
		return nil, errors.New("set TUDUDI_URL and TUDUDI_API_KEY")
	}
	return NewClient(base, key), nil
}

// parseArgs parses flags that come before, between or after positional
// arguments, so "add buy milk --json" works.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func printJSON(out io.Writer, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdAdd(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the created task as JSON")
	words, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(words, " "))
	if text == "" {
		return errors.New("usage: tududi-cli add <text...>")
	}

	a, err := c.Analyze(text)
	if err != nil {
		return err
	}
	body, err := taskFromAnalysis(c, text, a)
	if err != nil {
		return err
	}
	raw, err := c.CreateTask(body)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, raw)
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	fmt.Fprintf(out, "added %s %s\n", t.UID, t.DisplayName())
	return nil
}

// taskFromAnalysis builds the create body from tududi's reading of the text.
// An unknown +project is an error, so a typo does not file the task in the
// wrong place.
func taskFromAnalysis(c *Client, text string, a Analysis) (map[string]any, error) {
	name := strings.TrimSpace(a.CleanedContent)
	if name == "" {
		name = text
	}
	body := map[string]any{"name": name}
	if len(a.ParsedTags) > 0 {
		tags := make([]Tag, len(a.ParsedTags))
		for i, n := range a.ParsedTags {
			tags[i] = Tag{Name: n}
		}
		body["tags"] = tags
	}
	if len(a.ParsedProjects) > 0 {
		p, err := c.FindProject(a.ParsedProjects[0])
		if err != nil {
			return nil, err
		}
		body["project_uid"] = p.UID
	}
	if a.ParsedDueDate != nil && *a.ParsedDueDate != "" {
		body["due_date"] = dateOnly(*a.ParsedDueDate)
	}
	return body, nil
}

func dateOnly(s string) string {
	date, _, _ := strings.Cut(s, "T")
	return date
}

// listFlags are the filters shared by ls and pick.
type listFlags struct {
	project string
	tag     string
	today   bool
	done    bool
	all     bool
}

func (f *listFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.project, "project", "", "only tasks in this project (name)")
	fs.StringVar(&f.tag, "tag", "", "only tasks with these tags (comma-separated names)")
	fs.BoolVar(&f.today, "today", false, "only today's tasks")
	fs.BoolVar(&f.done, "done", false, "done and archived tasks instead of open ones")
	fs.BoolVar(&f.all, "all", false, "tasks of every status")
}

func (f *listFlags) query(c *Client) (url.Values, error) {
	q := url.Values{}
	q.Set("type", "all")
	if f.today {
		q.Set("type", "today")
	}
	switch {
	case f.all:
		q.Set("status", "all")
	case f.done:
		q.Set("status", "done")
	default:
		q.Set("status", "active")
	}
	if f.project != "" {
		p, err := c.FindProject(f.project)
		if err != nil {
			return nil, err
		}
		q.Set("project_uid", p.UID)
	}
	if f.tag != "" {
		q.Set("tags", f.tag)
	}
	return q, nil
}

func (f *listFlags) tasks(c *Client) (json.RawMessage, []Task, error) {
	q, err := f.query(c)
	if err != nil {
		return nil, nil, err
	}
	raw, err := c.ListTasks(q)
	if err != nil {
		return nil, nil, err
	}
	tasks, err := DecodeTasks(raw)
	return raw, tasks, err
}

// taskLine is one task on one line: uid, due date, name, +project, #tags.
func taskLine(t Task) string {
	parts := []string{t.UID, dashIfEmpty(dateOnly(t.DueDate)), shorten(t.DisplayName(), maxNameWidth)}
	var extra []string
	if t.Project != nil {
		extra = append(extra, "+"+t.Project.Name)
	}
	for _, tag := range t.Tags {
		extra = append(extra, "#"+tag.Name)
	}
	parts = append(parts, strings.Join(extra, " "))
	return strings.Join(parts, "\t")
}

// maxNameWidth keeps one long task name from widening every row of a list.
const maxNameWidth = 60

func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdList(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	var lf listFlags
	lf.register(fs)
	asJSON := fs.Bool("json", false, "print tududi's response as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	raw, tasks, err := lf.tasks(c)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, raw)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, t := range tasks {
		fmt.Fprintln(w, taskLine(t))
	}
	return w.Flush()
}

func cmdShow(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the task as JSON")
	uids, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(uids) != 1 {
		return errors.New("usage: tududi-cli show <uid>")
	}
	raw, err := c.GetTask(uids[0])
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, raw)
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	return showTask(c, t, out)
}

func showTask(c *Client, t Task, out io.Writer) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "name\t%s\n", t.DisplayName())
	fmt.Fprintf(w, "uid\t%s\n", t.UID)
	fmt.Fprintf(w, "status\t%s\n", t.StatusName())
	fmt.Fprintf(w, "priority\t%s\n", t.PriorityName())
	fmt.Fprintf(w, "due\t%s\n", dashIfEmpty(dateOnly(t.DueDate)))
	project := "-"
	if t.Project != nil {
		project = t.Project.Name
	}
	fmt.Fprintf(w, "project\t%s\n", project)
	tags := make([]string, len(t.Tags))
	for i, tag := range t.Tags {
		tags[i] = tag.Name
	}
	fmt.Fprintf(w, "tags\t%s\n", dashIfEmpty(strings.Join(tags, ", ")))
	fmt.Fprintf(w, "url\t%s\n", taskURL(c, t.UID))
	if err := w.Flush(); err != nil {
		return err
	}
	if t.Note != "" {
		fmt.Fprintf(out, "\n%s\n", t.Note)
	}
	return nil
}

func taskURL(c *Client, uid string) string {
	return c.BaseURL + "/task/" + url.PathEscape(uid)
}

func cmdDone(c *Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tududi-cli done <uid...>")
	}
	for _, uid := range args {
		if err := markDone(c, uid, out); err != nil {
			return err
		}
	}
	return nil
}

func markDone(c *Client, uid string, out io.Writer) error {
	if _, err := c.UpdateTask(uid, map[string]any{"status": statusNames[statusDone]}); err != nil {
		return err
	}
	fmt.Fprintf(out, "done %s\n", uid)
	return nil
}

func cmdRemove(c *Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tududi-cli rm <uid...>")
	}
	for _, uid := range args {
		if err := removeTask(c, uid, out); err != nil {
			return err
		}
	}
	return nil
}

func removeTask(c *Client, uid string, out io.Writer) error {
	if err := c.DeleteTask(uid); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", uid)
	return nil
}

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// resolveDue turns a --due value into what tududi stores. "none" clears the
// date, YYYY-MM-DD passes through, and anything else ("fri", "next week")
// goes through tududi's own date reading.
func resolveDue(c *Client, s string) (any, error) {
	if s == "none" {
		return nil, nil
	}
	if isoDate.MatchString(s) {
		return s, nil
	}
	a, err := c.Analyze("due " + s)
	if err != nil {
		return nil, err
	}
	if a.ParsedDueDate == nil || *a.ParsedDueDate == "" {
		return nil, fmt.Errorf("cannot read %q as a date", s)
	}
	return dateOnly(*a.ParsedDueDate), nil
}

func cmdEdit(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	name := fs.String("name", "", "new name")
	due := fs.String("due", "", `due date: YYYY-MM-DD, words like "fri", or "none" to clear`)
	priority := fs.String("priority", "", "low, medium or high")
	status := fs.String("status", "", strings.Join(statusNames, ", "))
	project := fs.String("project", "", "move to this project (name)")
	note := fs.String("note", "", "replace the note")
	tags := fs.String("tags", "", "replace the tags (comma-separated names)")
	asJSON := fs.Bool("json", false, "print the updated task as JSON")
	uids, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(uids) != 1 {
		return errors.New("usage: tududi-cli edit <uid> [flags]")
	}

	body := map[string]any{}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["name"] {
		body["name"] = *name
	}
	if set["due"] {
		d, err := resolveDue(c, *due)
		if err != nil {
			return err
		}
		body["due_date"] = d
	}
	if set["priority"] {
		if !slices.Contains(priorityNames, *priority) {
			return fmt.Errorf("priority must be one of %s", strings.Join(priorityNames, ", "))
		}
		body["priority"] = *priority
	}
	if set["status"] {
		if !slices.Contains(statusNames, *status) {
			return fmt.Errorf("status must be one of %s", strings.Join(statusNames, ", "))
		}
		body["status"] = *status
	}
	if set["project"] {
		p, err := c.FindProject(*project)
		if err != nil {
			return err
		}
		body["project_uid"] = p.UID
	}
	if set["note"] {
		body["note"] = *note
	}
	if set["tags"] {
		list := []Tag{}
		for _, n := range strings.Split(*tags, ",") {
			if n = strings.TrimSpace(n); n != "" {
				list = append(list, Tag{Name: n})
			}
		}
		body["tags"] = list
	}
	if len(body) == 0 {
		return errors.New("nothing to change, run tududi-cli edit -h")
	}

	raw, err := c.UpdateTask(uids[0], body)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, raw)
	}
	fmt.Fprintf(out, "updated %s\n", uids[0])
	return nil
}

func cmdPlan(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	date := fs.String("date", "today", "day of the plan: today or YYYY-MM-DD")
	asJSON := fs.Bool("json", false, "print tududi's response as JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if *date != "today" && !isoDate.MatchString(*date) {
		return errors.New("--date must be today or YYYY-MM-DD")
	}

	var raw json.RawMessage
	adding := len(rest) > 0 && rest[0] == "add"
	switch {
	case adding:
		if len(rest) < 2 {
			return errors.New("usage: tududi-cli plan add <uid...>")
		}
		raw, err = c.AddToPlan(*date, rest[1:])
	case len(rest) == 0:
		raw, err = c.GetPlan(*date)
	default:
		return errors.New("usage: tududi-cli plan [add <uid...>]")
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, raw)
	}

	var p DayPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if adding {
		fmt.Fprintf(out, "planned %d tasks for %s\n", len(rest)-1, p.Date)
		return nil
	}
	if p.Plan == nil {
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, item := range p.Plan.Items {
		start := "-"
		if item.StartMinute != nil {
			start = fmt.Sprintf("%02d:%02d", *item.StartMinute/60, *item.StartMinute%60)
		}
		fmt.Fprintf(w, "%s\t%dm\t%s\t%s\n", start, item.DurationMinutes, item.TaskUID, shorten(item.Task.DisplayName(), maxNameWidth))
	}
	return w.Flush()
}

func cmdPick(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pick", flag.ContinueOnError)
	var lf listFlags
	lf.register(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if _, err := exec.LookPath("fzf"); err != nil {
		return errors.New("pick needs fzf on PATH")
	}
	_, tasks, err := lf.tasks(c)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return errors.New("no tasks")
	}

	lines := make([]string, len(tasks))
	byUID := map[string]Task{}
	for i, t := range tasks {
		lines[i] = taskLine(t)
		byUID[t.UID] = t
	}
	choice, err := fzf(lines, "--delimiter=\t", "--with-nth=2..", "--prompt=task> ")
	if err != nil || choice == "" {
		return err
	}
	uid, _, _ := strings.Cut(choice, "\t")
	t := byUID[uid]

	action, err := fzf([]string{"done", "show", "open", "rm"}, "--prompt="+t.DisplayName()+"> ")
	if err != nil || action == "" {
		return err
	}
	switch action {
	case "done":
		return markDone(c, uid, out)
	case "show":
		return showTask(c, t, out)
	case "open":
		return openURL(taskURL(c, uid))
	case "rm":
		return removeTask(c, uid, out)
	}
	return nil
}

// fzf shows lines in fzf and returns the chosen line, or "" when the user
// pressed Esc.
func fzf(lines []string, args ...string) (string, error) {
	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n"))
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	var exitErr *exec.ExitError
	// fzf exits 130 on Esc and 1 when nothing matched.
	if errors.As(err, &exitErr) && (exitErr.ExitCode() == 130 || exitErr.ExitCode() == 1) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func openURL(u string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, u).Run()
}
