package client

// wantsHelp checks whether the first argument requests help (-h or --help), or if no arguments are provided.
func wantsHelp(args []string) bool {
	return len(args) == 0 || args[0] == "-h" || args[0] == "--help"
}
