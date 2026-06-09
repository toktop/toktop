package cli

// Info carries build/version metadata supplied by the binary's main package.
type Info struct {
	Name    string
	Version string
	Commit  string
	Date    string
}
