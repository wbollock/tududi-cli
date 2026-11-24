AI-generated content prepared on Will's behalf.

# tududi-cli

A command-line client for [tududi](https://github.com/chrisvel/tududi) tasks. It uses the tududi REST API with an API key.

## Install

With mise:

```sh
mise use -g github:wbollock/tududi-cli
```

With Go:

```sh
go install github.com/wbollock/tududi-cli@latest
```

## Configuration

Create an API key in tududi under Profile > API keys. Then set two environment variables:

```sh
export TUDUDI_URL=https://tududi.example.com
export TUDUDI_API_KEY=tt_...
```

## Usage

```sh
tududi-cli add "fix router #homelab +Infra tomorrow"
tududi-cli ls
tududi-cli ls --project Infra --tag homelab
tududi-cli ls --today
tududi-cli show <uid>
tududi-cli edit <uid> --due fri --priority high
tududi-cli done <uid>
tududi-cli rm <uid>
tududi-cli pick
tududi-cli plan
tududi-cli plan add <uid...>
```

`plan` shows today's Plan-your-day list. `plan add` adds tasks to it without a time slot, and skips tasks that are already on it. Use `--date YYYY-MM-DD` for another day.

`add` sends the text to tududi, which reads `#tags`, `+project` and dates the same way as quick capture in the web app. If the `+project` does not match a project name, the command stops and creates nothing. Put the text in quotes, because the shell reads a word that starts with `#` as a comment.

`pick` needs [fzf](https://github.com/junegunn/fzf). You choose a task, then choose to mark it done, show it, open it in the browser, or delete it.

`ls`, `show`, `add` and `edit` accept `--json`. The JSON is the tududi API response.

Run `tududi-cli <command> -h` to see the flags of a command.
