package forbiddencalls

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const (
	nativeCommandDir = "internal/tools/native/command"
	nativeCommandPkg = "github.com/lycaon/lycaon/internal/tools/native/command"
	cmdProgramPkg    = "github.com/lycaon/lycaon/internal/commandprogram"
	toolExecPkg      = "github.com/lycaon/lycaon/internal/toolexecution"
	bgProcessPkg     = "github.com/lycaon/lycaon/internal/bgprocess"
	internalExecPkg  = "github.com/lycaon/lycaon/internal/exec"
)

var recoveryRootNames = map[string]bool{
	"ProgramRuntime.RecoverProgramPublications": true,
	"ProgramRuntime.recoverProgramPublication":  true,
	"ProgramRuntime.materializeProgramOutputs":  true,
}

var forbiddenCommandEntrypoints = map[string]bool{
	"ProgramRuntime.Run":                true,
	"ProgramRuntime.beginProgram":       true,
	"ProgramRuntime.bindProgramSurface": true,
	"ProgramRuntime.settleProgram":      true,
	"ProgramDispatcher.Launch":          true,
	"ProgramDispatcher.Invoke":          true,
	"ProgramDispatcher.invokeNative":    true,
	"ProgramDispatcher.invokeExternal":  true,
	"ProgramDispatcher.invokeBuiltin":   true,
	"ProgramDispatcher.start":           true,
	"ProgramSupervisor.RunProgram":      true,
	"ProgramSupervisor.StartJob":        true,
	"CommandTool.Execute":               true,
	"CommandTool.Run":                   true,
	"RunCommand":                        true,
}

var publicationRecovery = []invariants.Invariant{{
	// Publication recovery replays sealed output publication only; it must not
	// reach ordinary program execution or process-launch entry points.
	Name: "publication-recovery-never-reexecutes-or-launches",
	Anchors: []invariants.Anchor{
		invariants.Func(nativeCommandDir, "ProgramRuntime.RecoverProgramPublications"),
		invariants.Func(nativeCommandDir, "ProgramRuntime.recoverProgramPublication"),
		invariants.Func(nativeCommandDir, "ProgramRuntime.materializeProgramOutputs"),
	},
	Check: checkPublicationRecoveryIsolation,
}}

func checkPublicationRecoveryIsolation(r *invariants.Run) {
	pkg := r.TypedPackage(nativeCommandDir)
	decls := map[*types.Func]*ast.FuncDecl{}
	var queue []*types.Func
	seen := map[*types.Func]bool{}

	for _, f := range pkg.Files {
		for _, decl := range f.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			obj, ok := pkg.Info.Defs[fn.Name].(*types.Func)
			if !ok {
				continue
			}
			decls[obj] = fn
			if recoveryRootNames[funcKey(fn)] {
				seen[obj] = true
				queue = append(queue, obj)
			}
		}
	}
	r.Expect(len(queue), "publication recovery root functions")

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		decl := decls[cur]
		if decl == nil || decl.Body == nil {
			continue
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if reason := forbiddenRecoveryFieldCall(pkg, call); reason != "" {
				r.ReportAt(call.Pos(), "publication recovery reaches %s via %s", reason, cur.Name())
				return true
			}
			callee := pkg.Callee(call)
			if callee == nil {
				return true
			}
			if reason := forbiddenRecoveryCallee(callee); reason != "" {
				r.ReportAt(call.Pos(), "publication recovery reaches %s via %s", reason, cur.Name())
				return true
			}
			if callee.Pkg() == pkg.Types && !seen[callee] {
				if _, hasBody := decls[callee]; hasBody {
					seen[callee] = true
					queue = append(queue, callee)
				}
			}
			return true
		})
	}
}

func funcKey(fn *ast.FuncDecl) string {
	if recv := invariants.ReceiverType(fn); recv != "" {
		return recv + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func typedFuncKey(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return fn.Name()
	}
	t := sig.Recv().Type()
	if ptr, isPtr := t.(*types.Pointer); isPtr {
		t = ptr.Elem()
	}
	if named, isNamed := t.(*types.Named); isNamed {
		return named.Obj().Name() + "." + fn.Name()
	}
	return fn.Name()
}

func forbiddenRecoveryFieldCall(pkg *invariants.TypedPackage, call *ast.CallExpr) string {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Evaluate" {
		return ""
	}
	s := pkg.Info.Selections[sel]
	if s == nil || s.Kind() != types.FieldVal {
		return ""
	}
	recv := s.Recv()
	if ptr, isPtr := recv.(*types.Pointer); isPtr {
		recv = ptr.Elem()
	}
	if named, ok := recv.(*types.Named); ok && named.Obj().Name() == "ProgramRuntime" {
		return "ProgramRuntime.Evaluate"
	}
	return ""
}

func forbiddenRecoveryCallee(fn *types.Func) string {
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	pkgPath := fn.Pkg().Path()
	name := fn.Name()
	key := typedFuncKey(fn)

	switch pkgPath {
	case nativeCommandPkg:
		if forbiddenCommandEntrypoints[key] {
			return fmt.Sprintf("command execution entrypoint %s", key)
		}
	case cmdProgramPkg:
		if strings.HasPrefix(name, "Evaluate") {
			return fmt.Sprintf("program evaluator %s.%s", pkgPath, name)
		}
	case toolExecPkg:
		if name == "Invoke" || name == "InvokeNative" || name == "Execute" || name == "Run" {
			return fmt.Sprintf("tool execution entrypoint %s.%s", pkgPath, key)
		}
	case bgProcessPkg:
		if name == "Start" || name == "StartProgram" || name == "Launch" {
			return fmt.Sprintf("background launch entrypoint %s.%s", pkgPath, key)
		}
	case internalExecPkg:
		if name == "PrepareCommand" || name == "StartPrepared" || name == "StartPipelineAsync" || name == "Start" || name == "Run" {
			return fmt.Sprintf("process launch entrypoint %s.%s", pkgPath, key)
		}
	case "os/exec":
		if name == "Command" || name == "CommandContext" || name == "Start" || name == "Run" || name == "Output" || name == "CombinedOutput" {
			return fmt.Sprintf("os/exec process launch %s", key)
		}
	case "os":
		if name == "StartProcess" {
			return "os.StartProcess"
		}
	case "syscall", "golang.org/x/sys/unix":
		if name == "ForkExec" || name == "StartProcess" || name == "Exec" {
			return fmt.Sprintf("%s.%s", pkgPath, name)
		}
	}
	return ""
}
