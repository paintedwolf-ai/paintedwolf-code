package forbiddencalls

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// portablePython holds the PY14 systemic structural invariants for Portable
// Python: guest-triggered HTTP must cross the mediated bridge, and guest
// workspace mounts must select the mediated editor/worker filesystem across
// both wazero and native-aot backends.
var portablePython = []invariants.Invariant{
	{
		// Guest-triggered HTTP must cross the product's mediated HTTP bridge.
		// Production entry points:
		//   - internal/portablepython.(*Service).ExecuteTool
		//   - internal/portablepython.(*GuestHTTPBridgeFS).OpenFile
		//   - internal/portablepython.(*httpBridgeFile).Read / Seek
		//   - internal/pythonadmission.(*GuestHTTPAdapter).HandleJSON / ReadStreamChunk
		// Approved door (traversal stops here):
		//   - internal/pythonadmission.(*HTTPTransport).Do
		//   - internal/pythonadmission.NewHTTPTransport
		// Staging artifact fetch (pythonruntime.FetchEngineArtifact, image_stage)
		// is unreachable from guest dispatch and therefore passes cleanly.
		Name: "portable-python-guest-http-uses-mediated-bridge",
		Anchors: []invariants.Anchor{
			invariants.Func("internal/portablepython", "Service.ExecuteTool"),
			invariants.Func("internal/portablepython", "NewGuestHTTPBridgeFS"),
			invariants.Func("internal/pythonadmission", "GuestHTTPAdapter.HandleJSON"),
			invariants.Func("internal/pythonadmission", "HTTPTransport.Do"),
		},
		Check: checkPortablePythonGuestHTTP,
	},
	{
		// Guest workspace mounts must select the mediated editor/worker
		// filesystem across both wazero and native-aot registration paths.
		// Raw host-directory mount constructors:
		//   - wazero.FSConfig.WithDirMount / WithReadOnlyDirMount
		//   - internal/pythonruntime.newHostDirSysFS / hostDirSysFS{}
		// Only explicit trusted image/private scratch mount roles are allowed:
		//   - "/usr/local/lib" (read-only stdlib)
		//   - DefaultGuestPackagesMount ("/packages", read-only bundled packages)
		//   - DefaultGuestScratchMount ("/tmp", private per-invocation scratch)
		Name: "portable-python-workspace-mounts-use-mediated-fs",
		Anchors: []invariants.Anchor{
			invariants.Func("internal/pythonruntime", "NewWorkspaceSysFS"),
			invariants.Func("internal/pythonruntime", "WazeroRuntime.buildModuleConfig"),
			invariants.Func("internal/pythonruntime", "NativeEngineRuntime.buildPreopens"),
			invariants.Func("internal/pythonruntime", "newHostDirSysFS"),
		},
		Check: checkPortablePythonWorkspaceMounts,
	},
}

type typedFuncBody struct {
	pkg  *invariants.TypedPackage
	decl *ast.FuncDecl
}

func indexTypedFuncBodies(prog *invariants.Program) map[*types.Func]typedFuncBody {
	index := make(map[*types.Func]typedFuncBody)
	for _, pkgPath := range prog.Order {
		pkg := prog.Packages[pkgPath]
		if pkg == nil || pkg.Info == nil {
			continue
		}
		for _, f := range pkg.Files {
			for _, decl := range f.AST.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if obj, ok := pkg.Info.Defs[fn.Name].(*types.Func); ok && obj != nil {
					index[obj] = typedFuncBody{pkg: pkg, decl: fn}
				}
			}
		}
	}
	return index
}

func canonicalFuncSymbol(fn *types.Func) (pkgPath, recvType, name string) {
	if fn == nil {
		return "", "", ""
	}
	if fn.Pkg() != nil {
		pkgPath = fn.Pkg().Path()
	}
	name = fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		t := sig.Recv().Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok && named.Obj() != nil {
			recvType = named.Obj().Name()
		}
	}
	return pkgPath, recvType, name
}

func isApprovedGuestHTTPDoor(modulePath string, fn *types.Func) bool {
	pkgPath, recv, name := canonicalFuncSymbol(fn)
	if pkgPath != modulePath+"/internal/pythonadmission" {
		return false
	}
	return (recv == "HTTPTransport" && name == "Do") || (recv == "" && name == "NewHTTPTransport")
}

func rawGuestHTTPBypassReason(fn *types.Func) string {
	pkgPath, recv, name := canonicalFuncSymbol(fn)
	switch pkgPath {
	case "net/http":
		switch {
		case recv == "" && (name == "Get" || name == "Post" || name == "PostForm" || name == "Head"):
			return "calls net/http." + name + " bypassing pythonadmission.HTTPTransport.Do"
		case recv == "Client" && (name == "Do" || name == "Get" || name == "Post" || name == "PostForm" || name == "Head"):
			return "calls (*net/http.Client)." + name + " bypassing pythonadmission.HTTPTransport.Do"
		case (recv == "Transport" || recv == "RoundTripper") && name == "RoundTrip":
			return "calls net/http." + recv + ".RoundTrip bypassing pythonadmission.HTTPTransport.Do"
		}
	case "net":
		switch {
		case recv == "" && (name == "Dial" || name == "DialTimeout" || name == "DialIP" || name == "DialTCP" || name == "DialUDP" || name == "DialUnix"):
			return "calls net." + name + " bypassing pythonadmission.HTTPTransport.Do"
		case recv == "Dialer" && (name == "Dial" || name == "DialContext"):
			return "calls (*net.Dialer)." + name + " bypassing pythonadmission.HTTPTransport.Do"
		}
	case "crypto/tls":
		switch {
		case recv == "" && (name == "Dial" || name == "DialWithDialer"):
			return "calls crypto/tls." + name + " bypassing pythonadmission.HTTPTransport.Do"
		case recv == "Dialer" && (name == "Dial" || name == "DialContext"):
			return "calls (*crypto/tls.Dialer)." + name + " bypassing pythonadmission.HTTPTransport.Do"
		}
	}
	return ""
}

func isGuestHTTPEntry(modulePath string, fn *types.Func) bool {
	pkgPath, recv, name := canonicalFuncSymbol(fn)
	switch pkgPath {
	case modulePath + "/internal/portablepython":
		return (recv == "Service" && name == "ExecuteTool") ||
			(recv == "GuestHTTPBridgeFS" && name == "OpenFile") ||
			(recv == "httpBridgeFile" && (name == "Read" || name == "Seek"))
	case modulePath + "/internal/pythonadmission":
		return recv == "GuestHTTPAdapter" && (name == "HandleJSON" || name == "ReadStreamChunk")
	}
	return false
}

func checkPortablePythonGuestHTTP(r *invariants.Run) {
	prog := r.Types()
	modulePath := r.Go().Module
	bodies := indexTypedFuncBodies(prog)

	var queue []*types.Func
	visited := make(map[*types.Func]bool)
	for fn := range bodies {
		if isGuestHTTPEntry(modulePath, fn) {
			visited[fn] = true
			queue = append(queue, fn)
		}
	}
	r.Expect(len(queue), "guest HTTP dispatch entry points")

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		body, ok := bodies[cur]
		if !ok {
			continue
		}
		ast.Inspect(body.decl.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				callee := body.pkg.Callee(node)
				if callee == nil {
					// Check calls through function-typed variables/fields resolving to net/http or net.
					if reason := checkIndirectRawHTTPCall(body.pkg, node); reason != "" {
						r.ReportAt(node.Pos(), "%s: %s", cur.FullName(), reason)
					}
					return true
				}
				if isApprovedGuestHTTPDoor(modulePath, callee) {
					// Stop traversal at the actual approved door.
					return true
				}
				if reason := rawGuestHTTPBypassReason(callee); reason != "" {
					r.ReportAt(node.Pos(), "%s: %s", cur.FullName(), reason)
					return true
				}
				if _, hasBody := bodies[callee]; hasBody && !visited[callee] {
					visited[callee] = true
					queue = append(queue, callee)
				}
			case *ast.SelectorExpr:
				if obj, ok := body.pkg.Info.Uses[node.Sel].(*types.Var); ok && obj != nil && obj.Pkg() != nil {
					if obj.Pkg().Path() == "net/http" && (obj.Name() == "DefaultClient" || obj.Name() == "DefaultTransport") {
						r.ReportAt(node.Pos(), "%s: references net/http.%s bypassing pythonadmission.HTTPTransport.Do", cur.FullName(), obj.Name())
					}
				}
			}
			return true
		})
	}
}

func checkIndirectRawHTTPCall(pkg *invariants.TypedPackage, call *ast.CallExpr) string {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	tv, ok := pkg.Info.Types[sel.X]
	if !ok || tv.Type == nil {
		return ""
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return ""
	}
	pkgPath := named.Obj().Pkg().Path()
	typeName := named.Obj().Name()
	if pkgPath == "net/http" && (typeName == "RoundTripper" || typeName == "Transport") && sel.Sel.Name == "RoundTrip" {
		return "calls net/http." + typeName + ".RoundTrip bypassing pythonadmission.HTTPTransport.Do"
	}
	return ""
}

// trustedRawMountRoles lists the exact guest mount paths that are permitted
// to use raw host-directory mounts (immutable stdlib, immutable bundled
// packages, and per-invocation private scratch).
var trustedRawMountRoles = map[string]bool{
	"/usr/local/lib": true,
	"/packages":      true,
	"/tmp":           true,
}

func resolveConstantGuestPath(pkg *invariants.TypedPackage, expr ast.Expr) (string, bool) {
	if expr == nil || pkg == nil || pkg.Info == nil {
		return "", false
	}
	if tv, ok := pkg.Info.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	return "", false
}

func isRawHostDirType(modulePath string, t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == modulePath+"/internal/pythonruntime" && named.Obj().Name() == "hostDirSysFS"
}

func isNewHostDirSysFS(modulePath string, fn *types.Func) bool {
	pkgPath, recv, name := canonicalFuncSymbol(fn)
	return pkgPath == modulePath+"/internal/pythonruntime" && recv == "" && name == "newHostDirSysFS"
}

func isNativePreopenType(modulePath string, t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == modulePath+"/internal/pythonruntime" && named.Obj().Name() == "nativePreopen"
}

// rawHostDirProducers returns every production function that directly or
// transitively constructs a raw hostDirSysFS (even if its return type is an
// interface like experimentalsys.FS).
func rawHostDirProducers(modulePath string, bodies map[*types.Func]typedFuncBody) map[*types.Func]bool {
	producers := make(map[*types.Func]bool)
	callers := make(map[*types.Func][]*types.Func)

	for fn, body := range bodies {
		if isNewHostDirSysFS(modulePath, fn) {
			producers[fn] = true
			continue
		}
		ast.Inspect(body.decl.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				callee := body.pkg.Callee(node)
				if callee == nil {
					return true
				}
				if isNewHostDirSysFS(modulePath, callee) {
					// Only mark fn as a producer if it returns a value (e.g. a helper
					// returning experimentalsys.FS or *hostDirSysFS), not a preopen
					// builder that binds trusted roles directly.
					if returnsFSOrHostDir(modulePath, fn) {
						producers[fn] = true
					}
					return true
				}
				if returnsFSOrHostDir(modulePath, fn) && returnsFSOrHostDir(modulePath, callee) {
					callers[callee] = append(callers[callee], fn)
				}
			case *ast.CompositeLit:
				if tv, ok := body.pkg.Info.Types[node]; ok && isRawHostDirType(modulePath, tv.Type) {
					if returnsFSOrHostDir(modulePath, fn) {
						producers[fn] = true
					}
				}
			}
			return true
		})
	}

	// Fixed-point propagate through helper chains.
	var queue []*types.Func
	for fn := range producers {
		queue = append(queue, fn)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, caller := range callers[cur] {
			if !producers[caller] {
				producers[caller] = true
				queue = append(queue, caller)
			}
		}
	}
	return producers
}

func returnsFSOrHostDir(modulePath string, fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Results() == nil || sig.Results().Len() == 0 {
		return false
	}
	for i := range sig.Results().Len() {
		rt := sig.Results().At(i).Type()
		if isRawHostDirType(modulePath, rt) {
			return true
		}
		if named, ok := rt.(*types.Named); ok && named.Obj() != nil && named.Obj().Pkg() != nil {
			if named.Obj().Pkg().Path() == "github.com/tetratelabs/wazero/experimental/sys" && named.Obj().Name() == "FS" {
				return true
			}
		}
	}
	return false
}

func exprIsRawHostDir(modulePath string, pkg *invariants.TypedPackage, expr ast.Expr, producers map[*types.Func]bool, rawVars map[types.Object]bool) bool {
	if expr == nil {
		return false
	}
	expr = ast.Unparen(expr)
	if tv, ok := pkg.Info.Types[expr]; ok && isRawHostDirType(modulePath, tv.Type) {
		return true
	}
	switch e := expr.(type) {
	case *ast.CallExpr:
		if callee := pkg.Callee(e); callee != nil && producers[callee] {
			return true
		}
	case *ast.Ident:
		if obj := pkg.Info.Uses[e]; obj != nil && rawVars[obj] {
			return true
		}
	}
	return false
}

func collectRawHostDirVars(modulePath string, pkg *invariants.TypedPackage, body *ast.BlockStmt, producers map[*types.Func]bool) map[types.Object]bool {
	rawVars := make(map[types.Object]bool)
	if body == nil {
		return rawVars
	}
	// Fixed-point over local assignments in the function body.
	changed := true
	for changed {
		changed = false
		ast.Inspect(body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i := 0; i < len(assign.Lhs) && i < len(assign.Rhs); i++ {
				id, ok := assign.Lhs[i].(*ast.Ident)
				if !ok {
					continue
				}
				if !exprIsRawHostDir(modulePath, pkg, assign.Rhs[i], producers, rawVars) {
					continue
				}
				obj := pkg.Info.Defs[id]
				if obj == nil {
					obj = pkg.Info.Uses[id]
				}
				if obj != nil && !rawVars[obj] {
					rawVars[obj] = true
					changed = true
				}
			}
			return true
		})
	}
	return rawVars
}

func checkPortablePythonWorkspaceMounts(r *invariants.Run) {
	prog := r.Types()
	modulePath := r.Go().Module
	bodies := indexTypedFuncBodies(prog)
	producers := rawHostDirProducers(modulePath, bodies)

	pkgs := r.TypedPackages("internal/pythonruntime", "internal/portablepython")
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.AST.Decls {
				fnDecl, ok := decl.(*ast.FuncDecl)
				if !ok || fnDecl.Body == nil {
					continue
				}
				curFn, _ := pkg.Info.Defs[fnDecl.Name].(*types.Func)
				rawVars := collectRawHostDirVars(modulePath, pkg, fnDecl.Body, producers)
				ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.CallExpr:
						checkMountCall(r, modulePath, pkg, curFn, node, producers, rawVars)
					case *ast.CompositeLit:
						checkPreopenComposite(r, modulePath, pkg, curFn, node, producers, rawVars)
					}
					return true
				})
			}
		}
	}
}

func checkMountCall(
	r *invariants.Run,
	modulePath string,
	pkg *invariants.TypedPackage,
	curFn *types.Func,
	call *ast.CallExpr,
	producers map[*types.Func]bool,
	rawVars map[types.Object]bool,
) {
	callee := pkg.Callee(call)
	if callee == nil {
		return
	}
	pkgPath, recv, name := canonicalFuncSymbol(callee)

	// Wazero backend raw host-directory mount constructors:
	//   FSConfig.WithDirMount(dir, guestPath)
	//   FSConfig.WithReadOnlyDirMount(dir, guestPath)
	if pkgPath == "github.com/tetratelabs/wazero" && recv == "FSConfig" && (name == "WithDirMount" || name == "WithReadOnlyDirMount") {
		if len(call.Args) < 2 {
			return
		}
		guestPath, ok := resolveConstantGuestPath(pkg, call.Args[1])
		if !ok || !trustedRawMountRoles[guestPath] {
			r.ReportAt(call.Pos(), "%s: wazero.%s mounts non-trusted guest path (only /usr/local/lib, /packages, /tmp may use raw host mounts; workspace must use WorkspaceSysFS)", funcNameOrAnon(curFn), name)
			return
		}
		if name == "WithDirMount" && guestPath != "/tmp" {
			r.ReportAt(call.Pos(), "%s: wazero.WithDirMount on %q must be read-only (only /tmp scratch is writable)", funcNameOrAnon(curFn), guestPath)
		}
		return
	}

	// Wazero backend experimental/sysfs.FSConfig.WithSysFSMount(fs, guestPath):
	// forbid binding a raw hostDirSysFS (directly or via helper) to a non-trusted path.
	if pkgPath == "github.com/tetratelabs/wazero/experimental/sysfs" && recv == "FSConfig" && name == "WithSysFSMount" {
		if len(call.Args) < 2 {
			return
		}
		if exprIsRawHostDir(modulePath, pkg, call.Args[0], producers, rawVars) {
			guestPath, ok := resolveConstantGuestPath(pkg, call.Args[1])
			if !ok || !trustedRawMountRoles[guestPath] {
				r.ReportAt(call.Pos(), "%s: WithSysFSMount binds raw hostDirSysFS to non-trusted guest path (workspace must use WorkspaceSysFS)", funcNameOrAnon(curFn))
			}
		}
		return
	}

	// Native-AOT backend: if newHostDirSysFS is called outside a trusted
	// nativePreopen role or a tracked helper, inspect at binding site;
	// additionally reject any direct hostDirSysFS composite literal outside
	// newHostDirSysFS itself.
	if isNewHostDirSysFS(modulePath, callee) && len(call.Args) >= 2 {
		// Checked at binding sites (nativePreopen / WithSysFSMount) via producers + rawVars;
		// also disallow calling newHostDirSysFS from portablepython directly.
		if strings.HasSuffix(pkg.Path, "/internal/portablepython") {
			r.ReportAt(call.Pos(), "%s: portablepython must not construct raw hostDirSysFS directly", funcNameOrAnon(curFn))
		}
	}
}

func checkPreopenComposite(
	r *invariants.Run,
	modulePath string,
	pkg *invariants.TypedPackage,
	curFn *types.Func,
	lit *ast.CompositeLit,
	producers map[*types.Func]bool,
	rawVars map[types.Object]bool,
) {
	tv, ok := pkg.Info.Types[lit]
	if !ok || tv.Type == nil {
		return
	}
	if isRawHostDirType(modulePath, tv.Type) && !isNewHostDirSysFS(modulePath, curFn) {
		r.ReportAt(lit.Pos(), "%s: constructs hostDirSysFS literal directly outside newHostDirSysFS", funcNameOrAnon(curFn))
		return
	}
	if !isNativePreopenType(modulePath, tv.Type) {
		return
	}
	var guestPathExpr, fsExpr ast.Expr
	for i, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok {
				switch id.Name {
				case "guestPath":
					guestPathExpr = kv.Value
				case "fs":
					fsExpr = kv.Value
				}
			}
		} else {
			switch i {
			case 0:
				guestPathExpr = elt
			case 1:
				fsExpr = elt
			}
		}
	}
	if !exprIsRawHostDir(modulePath, pkg, fsExpr, producers, rawVars) {
		return
	}
	guestPath, ok := resolveConstantGuestPath(pkg, guestPathExpr)
	if !ok || !trustedRawMountRoles[guestPath] {
		r.ReportAt(lit.Pos(), "%s: nativePreopen binds raw hostDirSysFS to non-trusted guest path (workspace must use WorkspaceSysFS)", funcNameOrAnon(curFn))
	}
}

func funcNameOrAnon(fn *types.Func) string {
	if fn == nil {
		return "<func>"
	}
	return fn.FullName()
}

var _ = token.NoPos
