package git

import "errors"

// configReadArgv confines one configuration query to repository-local values.
func configReadArgv(args []string) ([]string, error) {
	if len(args) == 2 && (args[1] == "--list" || args[1] == "-l") {
		return []string{"config", "--local", "--list"}, nil
	}
	if len(args) == 3 {
		switch args[1] {
		case "--get":
			return []string{"config", "--local", "--get", "--", args[2]}, nil
		case "--get-regexp":
			return []string{"config", "--local", "--get-regexp", "--", args[2]}, nil
		}
	}
	return nil, errors.New("git: config reads require one get, get-regexp or list action")
}
