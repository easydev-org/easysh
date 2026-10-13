package exec

// Vars returns a copy of the runner's variable bindings (for completion).
func (r *Runner) Vars() map[string]string {
	out := make(map[string]string, len(r.vars))
	for k, v := range r.vars {
		out[k] = v
	}
	return out
}

// Functions returns the names of defined functions.
func (r *Runner) Functions() map[string]bool {
	out := make(map[string]bool, len(r.funcs))
	for k := range r.funcs {
		out[k] = true
	}
	return out
}

// LastExit returns the last command's exit status.
func (r *Runner) LastExit() int { return r.lastExit }
