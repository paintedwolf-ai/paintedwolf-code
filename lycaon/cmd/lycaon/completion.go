package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/clisocket"
)

func runCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pw completion zsh|bash")
	}
	cache, err := clisocket.CompletionCachePath()
	if err != nil {
		return err
	}
	switch args[0] {
	case "zsh":
		fmt.Print(zshCompletion(cache))
	case "bash":
		fmt.Print(bashCompletion(cache))
	default:
		return fmt.Errorf("unknown shell %q (want zsh or bash)", args[0])
	}
	return nil
}

// writeCompletionCache records project names for the shell to complete against,
// one per line.
//
// Names holding spaces complete only as far as their first word under bash,
// whose compgen -W splits on IFS. zsh reads the file as newline-separated and
// handles them whole.
func writeCompletionCache(rows []clisocket.ProjectRow) error {
	path, err := clisocket.CompletionCachePath()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if name := strings.TrimSpace(row.Name); name != "" {
			names = append(names, name)
		}
	}
	body := strings.Join(names, "\n")
	if body != "" {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// shellQuote wraps a path for safe interpolation into the emitted script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// zshCompletion is emitted for `eval "$(pw completion zsh)"`.
//
// The cache path is baked in at generation time: the config dir varies by
// compiled channel and, in development builds, by LYCAON_CONFIG_DIR.
func zshCompletion(cache string) string {
	return `_pw_project_names() {
  local cache=` + shellQuote(cache) + `
  [[ -r $cache ]] || return 1
  local -a names
  names=("${(@f)$(<$cache)}")
  compadd -a names
}

_pw() {
  if (( CURRENT == 2 )); then
    _alternative \
      "commands:command:((open\:'Open a project by folder or name' ls\:'List projects' logs\:'Browse debug capture logs'))" \
      'projects:project:_pw_project_names' \
      'directories:directory:_files -/'
    return
  fi
  case ${words[2]} in
    open)
      _alternative \
        'projects:project:_pw_project_names' \
        'directories:directory:_files -/'
      ;;
  esac
}

compdef _pw pw
`
}

// bashCompletion is emitted for `eval "$(pw completion bash)"`.
func bashCompletion(cache string) string {
	return `_pw() {
  local cur cache names
  cur="${COMP_WORDS[COMP_CWORD]}"
  cache=` + shellQuote(cache) + `
  names=""
  [[ -r $cache ]] && names=$(cat "$cache")
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "open ls logs $names" -- "$cur") $(compgen -d -- "$cur") )
    return
  fi
  if [[ ${COMP_WORDS[1]} == "open" ]]; then
    COMPREPLY=( $(compgen -W "$names" -- "$cur") $(compgen -d -- "$cur") )
  fi
}

complete -F _pw pw
`
}
