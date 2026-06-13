package trace

import "toktop.unceas.dev/internal/textutil"

// ClipToolCalls bounds each tool call's Input/Output to maxBytes via
// textutil.ClipText, in place. maxBytes <= 0 leaves everything untouched. The
// export (cli) and handoff paths both inline tool output and share this one
// definition instead of carrying byte-identical per-package loops.
func ClipToolCalls(turns []Turn, maxBytes int) {
	if maxBytes <= 0 {
		return
	}
	for ti := range turns {
		calls := turns[ti].ToolCalls
		for ci := range calls {
			calls[ci].Output, _ = textutil.ClipText(calls[ci].Output, maxBytes)
			calls[ci].Input, _ = textutil.ClipText(calls[ci].Input, maxBytes)
		}
	}
}
