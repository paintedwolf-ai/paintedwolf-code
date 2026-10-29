package commandprogram

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func catalogCommand(t *testing.T, name string) toolcontract.CommandSpecification {
	t.Helper()
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, command := range catalog.Commands {
		if command.Name == name {
			return command
		}
	}
	t.Fatalf("catalog has no %s command", name)
	return toolcontract.CommandSpecification{}
}

func argvFields(words ...string) Fields {
	fields := make(Fields, len(words))
	for i, word := range words {
		fields[i] = Field{Value: word, Span: toolcontract.SourceSpan{StartByte: i * 10, EndByte: i*10 + len(word), Line: 1, Column: i*10 + 1}}
	}
	return fields
}

// boundShape renders options as key[values]terminator and operands as =value.
func boundShape(b BoundArgv) []string {
	var out []string
	for _, item := range b.Items {
		if item.Operand != nil {
			out = append(out, "="+item.Operand.Value)
			continue
		}
		option := item.Option
		shape := option.Key + "[" + strings.Join(option.Values.Values(), ",") + "]" + option.Terminator
		if option.Scope == "global" {
			shape = "global:" + shape
		}
		out = append(out, shape)
	}
	return out
}

func TestCatalogBinderBindsEveryCatalogOptionFragment(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	commands := map[string]toolcontract.CommandSpecification{}
	forms := map[string]toolcontract.OptionForm{}
	for _, command := range catalog.Commands {
		commands[command.Name] = command
		for _, form := range command.Forms {
			forms[command.Name+"\x00"+form.FixtureID] = form
		}
	}
	checked := 0
	for _, fixture := range catalog.Fixtures {
		if fixture.Kind != "option-fragment" {
			continue
		}
		command, ok := commands[fixture.Command]
		if !ok {
			t.Fatalf("%s names unknown command %s", fixture.ID, fixture.Command)
		}
		id := strings.TrimSuffix(fixture.ID, "-missing-value")
		if i := strings.Index(id, "-alias-"); i >= 0 {
			id = id[:i]
		}
		form, known := forms[command.Name+"\x00"+id]
		names := strings.Fields(command.Name)
		words := append([]string{}, names...)
		if known && form.Scope == "global" {
			words = append(append(names[:1:1], fixture.Words...), names[1:]...)
		} else {
			words = append(words, fixture.Words...)
		}
		bound, err := CatalogBinder{}.Bind(command, argvFields(words...))
		checked++
		if fixture.Diagnostic != "" {
			var argvErr *ArgvError
			if !errors.As(err, &argvErr) || argvErr.Code != fixture.Diagnostic || argvErr.Command != command.Name {
				t.Errorf("%s: %v, want %s", fixture.ID, err, fixture.Diagnostic)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", fixture.ID, err)
			continue
		}
		if len(bound.Items) != 1 || bound.Items[0].Option == nil {
			t.Errorf("%s bound %v", fixture.ID, boundShape(bound))
			continue
		}
		option := bound.Items[0].Option
		values := len(fixture.Words) - 1
		if fixture.Arity == -2 {
			values--
		}
		optionWord := len(names)
		if form.Scope == "global" {
			optionWord = 1
		}
		if option.Key != fixture.ExpectedForm || len(option.Values) != values || option.Scope != form.Scope || option.Span != argvFields(words...)[optionWord].Span {
			t.Errorf("%s bound %+v", fixture.ID, *option)
		}
	}
	if checked < 700 {
		t.Fatalf("checked only %d catalog option fragments", checked)
	}
}

func TestCatalogBinderOptionGrammar(t *testing.T) {
	for _, c := range []struct {
		name, command string
		argv          []string
		want          []string
		code          string
	}{
		{name: "separate value", command: "head", argv: []string{"head", "-n", "5", "file"}, want: []string{"-n[5]", "=file"}},
		{name: "attached short value", command: "head", argv: []string{"head", "-n5"}, want: []string{"-n[5]"}},
		{name: "legacy numeric shorthand", command: "head", argv: []string{"head", "-12", "file"}, want: []string{"-n[12]", "=file"}},
		{name: "long equals value", command: "head", argv: []string{"head", "--lines=3"}, want: []string{"-n[3]"}},
		{name: "long separate value", command: "head", argv: []string{"head", "--bytes", "7"}, want: []string{"-c[7]"}},
		{name: "cluster ending in value", command: "head", argv: []string{"head", "-qn", "3", "a", "b"}, want: []string{"-q[]", "-n[3]", "=a", "=b"}},
		{name: "cluster with attached value", command: "head", argv: []string{"head", "-qc9"}, want: []string{"-q[]", "-c[9]"}},
		{name: "double dash ends options", command: "head", argv: []string{"head", "--", "-5", "-"}, want: []string{"=-5", "=-"}},
		{name: "dash and empty are operands", command: "cat", argv: []string{"cat", "-", ""}, want: []string{"=-", "="}},
		{name: "numeric shorthand needs digits", command: "head", argv: []string{"head", "-5x"}, code: argvOptionUnsupported},
		{name: "plus start value", command: "tail", argv: []string{"tail", "-n", "+5"}, want: []string{"-n[+5]"}},
		{name: "options after operands permute", command: "grep", argv: []string{"grep", "needle", "-n", "file"}, want: []string{"=needle", "-n[]", "=file"}},
		{name: "value consumes dash word", command: "grep", argv: []string{"grep", "-e", "-x", "--", "-file"}, want: []string{"-e[-x]", "=-file"}},
		{name: "boolean cluster", command: "ls", argv: []string{"ls", "-laR"}, want: []string{"-l[]", "-a[]", "-R[]"}},
		{name: "digit spelling is exact", command: "ls", argv: []string{"ls", "-1"}, want: []string{"-1[]"}},
		{name: "cluster then next-word value", command: "sort", argv: []string{"sort", "-rnk", "2", "-t:"}, want: []string{"-r[]", "-n[]", "-k[2]", "-t[:]"}},
		{name: "attached separators", command: "cut", argv: []string{"cut", "-d,", "-f2"}, want: []string{"-d[,]", "-f[2]"}},
		{name: "attached mode", command: "nl", argv: []string{"nl", "-ba"}, want: []string{"-b[a]"}},
		{name: "bsd in-place empty suffix", command: "sed", argv: []string{"sed", "-i", "", "-e", "s/a/b/", "f"}, want: []string{"-i[]", "-e[s/a/b/]", "=f"}},
		{name: "gnu in-place leaves script", command: "sed", argv: []string{"sed", "-i", "s/a/b/", "f"}, want: []string{"-i[]", "=s/a/b/", "=f"}},
		{name: "in-place in cluster", command: "sed", argv: []string{"sed", "-ni", "", "p", "f"}, want: []string{"-n[]", "-i[]", "=p", "=f"}},
		{name: "backup suffix unsupported", command: "sed", argv: []string{"sed", "-i.bak", "p"}, code: argvOptionUnsupported},
		{name: "extended alias", command: "sed", argv: []string{"sed", "-r", "p"}, want: []string{"-E[]", "=p"}},
		{name: "optional attached long value", command: "git status", argv: []string{"git", "status", "--porcelain=v1"}, want: []string{"--porcelain[v1]"}},
		{name: "optional value never takes next word", command: "git status", argv: []string{"git", "status", "--porcelain", "v1"}, want: []string{"--porcelain[]", "=v1"}},
		{name: "global options before subcommand", command: "git status", argv: []string{"git", "-C", "dir", "-c", "color.ui=false", "status", "-s", "--", "a"}, want: []string{"global:-C[dir]", "global:-c[color.ui=false]", "-s[]", "=a"}},
		{name: "global form refused after subcommand", command: "git status", argv: []string{"git", "status", "-C", "dir"}, code: argvOptionUnsupported},
		{name: "subcommand required", command: "git status", argv: []string{"git", "-C", "dir"}, code: argvOperandInvalid},
		{name: "wrong subcommand", command: "git status", argv: []string{"git", "log"}, code: argvOperandInvalid},
		{name: "same spelling in two scopes", command: "git switch", argv: []string{"git", "-c", "k=v", "switch", "-c", "topic"}, want: []string{"global:-c[k=v]", "-c[topic]"}},
		{name: "subcommand numeric shorthand", command: "git log", argv: []string{"git", "log", "-3", "--oneline"}, want: []string{"-n[3]", "--oneline[]"}},
		{name: "generic git stops at subcommand", command: "git", argv: []string{"git", "-C", "dir", "frobnicate", "-x", "--y"}, want: []string{"-C[dir]", "=frobnicate", "=-x", "=--y"}},
		{name: "signal word shorthand", command: "kill", argv: []string{"kill", "-TERM", "job"}, want: []string{"-s[TERM]", "=job"}},
		{name: "signal number shorthand", command: "kill", argv: []string{"kill", "-9", "--", "job"}, want: []string{"-s[9]", "=job"}},
		{name: "signal option attached", command: "kill", argv: []string{"kill", "-sINT", "job"}, want: []string{"-s[INT]", "=job"}},
		{name: "operand ends xargs options", command: "xargs", argv: []string{"xargs", "-0", "-n", "1", "grep", "-r", "x"}, want: []string{"-0[]", "-n[1]", "=grep", "=-r", "=x"}},
		{name: "operand ends env options", command: "env", argv: []string{"env", "-i", "-u", "HOME", "A=1", "cmd", "-i"}, want: []string{"-i[]", "-u[HOME]", "=A=1", "=cmd", "=-i"}},
		{name: "two-word option value", command: "jq", argv: []string{"jq", "--arg", "name", "value", "-r", ".x"}, want: []string{"--arg[name,value]", "-r[]", "=.x"}},
		{name: "two-word option missing word", command: "jq", argv: []string{"jq", "--arg", "name"}, code: argvOperandInvalid},
		{name: "two-word option refuses equals", command: "jq", argv: []string{"jq", "--arg=name", "value"}, code: argvOperandInvalid},
		{name: "boolean long refuses value", command: "jq", argv: []string{"jq", "--slurp=1"}, code: argvOperandInvalid},
		{name: "unknown long option", command: "jq", argv: []string{"jq", "--slur"}, code: argvOptionUnsupported},
		{name: "unknown short in cluster", command: "ls", argv: []string{"ls", "-lZ"}, code: argvOptionUnsupported},
		{name: "find primaries and operators", command: "find", argv: []string{"find", ".", "-name", "*.go", "-o", "!", "-type", "d", "-print0"}, want: []string{"=.", "-name[*.go]", "-o[]", "=!", "-type[d]", "-print0[]"}},
		{name: "find exec single", command: "find", argv: []string{"find", ".", "-exec", "rm", "{}", ";", "-print"}, want: []string{"=.", "-exec[rm,{}];", "-print[]"}},
		{name: "find exec batch", command: "find", argv: []string{"find", "-exec", "echo", "+", "{}", "+"}, want: []string{"-exec[echo,+,{}]+"}},
		{name: "find exec needs terminator", command: "find", argv: []string{"find", "-exec", "rm", "{}"}, code: argvOperandInvalid},
		{name: "find exec needs command", command: "find", argv: []string{"find", "-exec", ";"}, code: argvOperandInvalid},
		{name: "find primaries do not cluster", command: "find", argv: []string{"find", "-ao"}, code: argvOptionUnsupported},
		{name: "find primaries take separate values", command: "find", argv: []string{"find", "-typef"}, code: argvOptionUnsupported},
		{name: "listener option attached", command: "lsof", argv: []string{"lsof", "-iTCP:8080", "-sTCP:LISTEN"}, want: []string{"-i[TCP:8080]", "-s[TCP:LISTEN]"}},
		{name: "dual dialect spellings", command: "base64", argv: []string{"base64", "-D", "-b", "76"}, want: []string{"-d[]", "-b[76]"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			bound, err := CatalogBinder{}.Bind(catalogCommand(t, c.command), argvFields(c.argv...))
			if c.code != "" {
				var argvErr *ArgvError
				if !errors.As(err, &argvErr) || argvErr.Code != c.code {
					t.Fatalf("err=%v bound=%v, want %s", err, boundShape(bound), c.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("bind: %v", err)
			}
			if got := boundShape(bound); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("bound %q, want %q", got, c.want)
			}
		})
	}
}

func TestCatalogBinderReportsWordSpanAndCommandIdentity(t *testing.T) {
	argv := argvFields("ls", "-l", "-lZ")
	_, err := CatalogBinder{}.Bind(catalogCommand(t, "ls"), argv)
	var argvErr *ArgvError
	if !errors.As(err, &argvErr) || argvErr.Word != "-lZ" || argvErr.Span != argv[2].Span || !strings.Contains(argvErr.Error(), "COMMAND_OPTION_UNSUPPORTED") {
		t.Fatalf("diagnostic %+v", err)
	}
	if _, err := (CatalogBinder{}).Bind(catalogCommand(t, "ls"), argvFields("cat", "-l")); err == nil || errors.As(err, &argvErr) && argvErr.Word == "cat" {
		t.Fatalf("other command word bound: %v", err)
	}
	if _, err := (CatalogBinder{}).Bind(catalogCommand(t, "ls"), nil); err == nil {
		t.Fatal("empty argv bound")
	}
	bound, err := CatalogBinder{}.Bind(catalogCommand(t, "repo-map"), argvFields("survey-repo", "--depth", "2"))
	if err != nil || bound.Command != "repo-map" {
		t.Fatalf("alias: %v %+v", err, bound)
	}
}

func TestBoundArgvAccessorsAndReviewProjection(t *testing.T) {
	argv := argvFields("git", "-C", "dir", "switch", "-c", "one", "-c", "two", "--", "-ref")
	bound, err := CatalogBinder{}.Bind(catalogCommand(t, "git switch"), argv)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !bound.EndOfOptions || !reflect.DeepEqual(bound.Operands().Values(), []string{"-ref"}) {
		t.Fatalf("operands %v end=%v", bound.Operands().Values(), bound.EndOfOptions)
	}
	if len(bound.Options("-c")) != 2 || len(bound.Global("-C")) != 1 || len(bound.Global("-c")) != 0 || len(bound.Options("-C")) != 0 {
		t.Fatalf("scoped lookups %v", boundShape(bound))
	}
	last, ok := bound.Last("-c")
	if !ok || last.Values[0].Value != "two" || last.Values[0].Span != argv[7].Span || last.Span != argv[6].Span {
		t.Fatalf("last %+v", last)
	}
	if _, ok := bound.Last("-x"); ok {
		t.Fatal("absent option found")
	}
	review := bound.Review("git_switch")
	want := ArgvReview{OperationID: "git_switch", Command: "git switch", Operands: []string{"-ref"}, Options: []ReviewedOption{
		{Option: "-C", Scope: "global", ValueRole: "path", Values: []string{"dir"}},
		{Option: "-c", Scope: "subcommand", ValueRole: review.Options[1].ValueRole, Values: []string{"one"}},
		{Option: "-c", Scope: "subcommand", ValueRole: review.Options[1].ValueRole, Values: []string{"two"}},
	}}
	if !reflect.DeepEqual(review, want) || review.Options[1].ValueRole == "" {
		t.Fatalf("review %+v", review)
	}
	empty, err := CatalogBinder{}.Bind(catalogCommand(t, "pwd"), argvFields("pwd"))
	if err != nil || empty.Review("pwd").Options == nil || len(empty.Review("pwd").Operands) != 0 {
		t.Fatalf("empty review %+v %v", empty.Review("pwd"), err)
	}
}
