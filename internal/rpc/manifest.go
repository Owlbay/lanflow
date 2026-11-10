package rpc

import "sort"

// Manifest lists every method of the local interface: the app.API methods
// (manifest_gen.go, generated from internal/app/api.go and levels.go) and
// the agent methods (agentops.go), by name.
func Manifest() []Method {
	out := make([]Method, 0, len(apiMethods)+len(agentOps))
	out = append(out, apiMethods...)
	for _, op := range agentOps {
		out = append(out, op.Method)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the manifest entry of name.
func Lookup(name string) (Method, bool) {
	if m, ok := apiMethod(name); ok {
		return m, true
	}
	if op, ok := agentOps[name]; ok {
		return op.Method, true
	}
	return Method{}, false
}

var apiIndex = func() map[string]int {
	m := make(map[string]int, len(apiMethods))
	for i, x := range apiMethods {
		m[x.Name] = i
	}
	return m
}()

func apiMethod(name string) (Method, bool) {
	i, ok := apiIndex[name]
	if !ok {
		return Method{}, false
	}
	return apiMethods[i], true
}

// LevelOf is the level levels.go gives an app.API method ("" = missing).
func LevelOf(name string) Level { return methodLevels[name] }
